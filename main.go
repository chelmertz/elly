package main

import (
	"errors"
	"flag"
	"fmt"
	"net"
	neturl "net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"log/slog"

	"github.com/chelmertz/elly/internal/backoff"
	"github.com/chelmertz/elly/internal/github"
	"github.com/chelmertz/elly/internal/notify"
	"github.com/chelmertz/elly/internal/points"
	"github.com/chelmertz/elly/internal/server"
	"github.com/chelmertz/elly/internal/storage"
	"github.com/chelmertz/elly/internal/types"
)

var timeoutMinutes = flag.Int("timeout", 5, "refresh PRs every N minutes")
var url = flag.String("url", "localhost:9876", "URL for web GUI")
var pageUrl = flag.String("page-url", "", "the page's address in notifications, e.g. http://elly.localhost:9876 (default: http:// and -url)")
var dbPath = flag.String("db", "", "path to SQLite database file (default: OS-appropriate data dir)")
var demo = flag.Bool("demo", false, "mock the PRs so you can take a proper screenshot of the GUI")
var versionFlag = flag.Bool("version", false, "show version")
var verboseFlag = flag.Bool("verbose", false, "verbose logging")
var notifyFlag = flag.Bool("notify", false, "send a desktop notification (notify-send) when a PR gains a reason to act")
var annotationSchemes = flag.String("annotation-schemes", "", "comma-separated link schemes annotations may use besides https, e.g. claude-review")
var annotationTokenFile = flag.String("annotation-token-file", "", "where to write the bearer token annotation writes need (default: $XDG_RUNTIME_DIR/elly/annotation-token; no XDG_RUNTIME_DIR disables annotation writes)")
var recordDir = flag.String("record", "", "development: run one poll, write it anonymised to the next free poll-N.json in this directory, and exit")
var recordOwners = flag.String("record-owners", "", "comma-separated repo owners -record keeps; every other PR is left out of the recording")
var allowedHosts = flag.String("allowed-hosts", "", "comma-separated Host values elly answers besides loopback names on the listen port, e.g. elly.example behind a reverse proxy (port included unless it is the scheme's default)")
var githubAPIFlag = flag.String("github-api", github.DefaultAPIURL, "development: the GitHub API to poll and check tokens with, e.g. a stub on a loopback address")
var workOwners = flag.String("work-owners", "", "comma-separated repo owners that notify only on reasons to act; any other owner notifies on every change by someone else")

func main() {
	flag.Parse()

	logLevel := &slog.LevelVar{}
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{AddSource: true, Level: logLevel}))

	var version string
	if bi, ok := debug.ReadBuildInfo(); ok {
		version = bi.Main.Version
	}
	if version == "" {
		version = "unknown"
	}

	if *versionFlag {
		fmt.Println(version)
		os.Exit(0)
	}

	if *verboseFlag {
		logLevel.Set(slog.LevelDebug)
	} else {
		logLevel.Set(slog.LevelInfo)
	}

	if *dbPath == "" {
		*dbPath = defaultDBPath()
	}

	apiURL, err := githubAPI(*githubAPIFlag)
	if err != nil {
		logger.Error("bad flag", slog.Any("error", err))
		os.Exit(2)
	}

	if *recordDir != "" {
		path, err := record(*recordDir, splitOwners(*recordOwners), *verboseFlag, *dbPath, apiURL, logger)
		if err != nil {
			logger.Error("could not record a poll", slog.Any("error", err))
			os.Exit(1)
		}
		logger.Info("recorded a poll", "file", path)
		os.Exit(0)
	}
	dbDir := filepath.Dir(*dbPath)
	if err := os.MkdirAll(dbDir, 0o700); err != nil {
		logger.Error("could not create database directory", "dir", dbDir, "error", err)
		os.Exit(1)
	}

	var store storage.Storage
	if *demo {
		store = storage.NewStorageDemo()
	} else {
		store = storage.NewStorage(logger, *dbPath)
		// The database holds GitHub tokens. sqlite creates it, and its WAL
		// and shm files, under the umask, and MkdirAll leaves a directory
		// that already existed as it was.
		if err := restrictDBFiles(*dbPath); err != nil {
			logger.Error("could not restrict the database files to this user", slog.Any("error", err))
			os.Exit(1)
		}
	}

	// every write the page shows tells open pages to re-render
	changes := server.NewChanges()
	store = storage.Notifying(store, changes.Changed)

	setupMode, err := initPAT(store, apiURL, logger)
	if err != nil {
		logger.Error("failed to initialize PAT", slog.Any("error", err))
		os.Exit(1)
	}

	if setupMode {
		logger.Info("starting elly in setup mode", "version", version, "db", *dbPath)
	} else {
		logger.Info("starting elly", "version", version, "db", *dbPath, "timeout_minutes", *timeoutMinutes, "demo", *demo)
	}

	tracker := backoff.New(logger, time.Duration(*timeoutMinutes)*time.Minute)

	var send func(notify.Notification)
	if *notifyFlag {
		bin, err := exec.LookPath("notify-send")
		if err != nil {
			logger.Error("-notify needs notify-send on PATH", slog.Any("error", err))
			os.Exit(1)
		}
		send = notify.Sender{Bin: bin, PageUrl: pageAddress(*pageUrl, *url), Logger: logger}.Send
	}

	go startRefreshLoop(store, tracker, apiURL, send, logger)

	annotationToken := ""
	if path := annotationTokenPath(*annotationTokenFile, os.Getenv("XDG_RUNTIME_DIR")); path != "" {
		annotationToken, err = server.WriteAnnotationToken(path)
		if err != nil {
			logger.Error("could not write annotation token", slog.Any("error", err))
			os.Exit(1)
		}
	} else {
		logger.Info("annotation writes disabled: no -annotation-token-file and no XDG_RUNTIME_DIR")
	}

	server.ServeWeb(server.HttpServerConfig{
		Url:               *url,
		Store:             store,
		Tracker:           tracker,
		TimeoutMinutes:    *timeoutMinutes,
		Version:           version,
		Logger:            logger,
		SetupMode:         setupMode,
		AnnotationToken:   annotationToken,
		AnnotationSchemes: splitOwners(*annotationSchemes),
		AllowedHosts:      splitOwners(*allowedHosts),
		GithubURL:         apiURL,
		Changes:           changes,
	})
}

// restrictDBFiles makes the database and the files sqlite keeps beside it
// readable by this user only. The directory is left alone: -db may point into
// one that is not elly's.
func restrictDBFiles(dbPath string) error {
	for _, f := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		if err := os.Chmod(f, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// initPAT initializes the PAT from env var or storage.
// Returns (setupMode, error) where setupMode=true means no valid PAT is configured.
func initPAT(store storage.Storage, githubBaseURL string, logger *slog.Logger) (bool, error) {
	envToken := os.Getenv("GITHUB_PAT")
	if envToken != "" {
		os.Unsetenv("GITHUB_PAT") //nolint:errcheck // best-effort security cleanup
		username, expiresAt, err := github.ValidatePAT(githubBaseURL, envToken, logger)
		if err != nil {
			logger.Warn("GITHUB_PAT env var is invalid, falling back to stored PAT", slog.Any("error", err))
		} else {
			if err := store.StorePAT(envToken, username, expiresAt); err != nil {
				return false, fmt.Errorf("could not store PAT from env var: %w", err)
			}
			return false, nil
		}
	}

	storedPat, found, err := store.GetPAT()
	if err != nil {
		logger.Warn("could not read stored PAT, starting in setup mode", slog.Any("error", err))
		return true, nil
	}
	if !found {
		logger.Info("no PAT configured, starting in setup mode")
		return true, nil
	}

	// Validate stored PAT
	_, expiresAt, err := github.ValidatePAT(githubBaseURL, storedPat.Token, logger)
	if err != nil {
		if errors.Is(err, github.ErrInvalidToken) {
			logger.Warn("stored PAT is no longer valid, starting in setup mode", slog.Any("error", err))
			if clearErr := store.ClearPAT(); clearErr != nil {
				logger.Warn("could not clear invalid PAT", slog.Any("error", clearErr))
			}
			return true, nil
		}
		// Anything but a 401 (rate limit, 403, 429, 5xx, a page that is not
		// JSON, the network) is transient: keep the PAT and poll as usual.
		logger.Warn("could not validate stored PAT due to transient error, assuming it is still valid", slog.Any("error", err))
		return false, nil
	}
	backfillExpiries(store, githubBaseURL, storedPat, expiresAt, logger)
	return false, nil
}

// backfillExpiries stores the expiry of each token that has none stored, from
// the header GitHub answers every request with: a token stored before elly
// read it would otherwise never warn before it expires. A token's expiry is
// fixed (regenerating makes a new token), so startup is often enough.
func backfillExpiries(store storage.Storage, baseURL string, primary storage.StoredPAT, primaryExpiry time.Time, logger *slog.Logger) {
	if primary.ExpiresAt.IsZero() && !primaryExpiry.IsZero() {
		if err := store.SetPATExpiry(primary.Token, primaryExpiry); err != nil {
			logger.Warn("could not store the token's expiry", slog.Any("error", err))
		}
	}
	owners, err := store.OwnerPATs()
	if err != nil {
		logger.Warn("could not read the owner tokens", slog.Any("error", err))
		return
	}
	for _, o := range owners {
		if !o.ExpiresAt.IsZero() || o.Refused != "" {
			continue
		}
		_, expiry, err := github.UsernameFromPat(baseURL, o.Token, logger)
		if err != nil || expiry.IsZero() {
			continue
		}
		if err := store.SetOwnerPATExpiry(o.Owner, o.Token, expiry); err != nil {
			logger.Warn("could not store an owner token's expiry", slog.String("owner", o.Owner), slog.Any("error", err))
		}
	}
}

// pollDue says whether a timer tick at now polls, given the last poll. A poll
// is stored when it finishes, seconds after its tick, so the next tick finds
// it a little under one interval old; only a poll younger than half the
// interval, as after a restart, is skipped.
func pollDue(last, now time.Time, interval time.Duration) bool {
	return now.Sub(last) >= interval/2
}

// findOwner records which owner pat belongs to, so the page names it as it
// names every other token. GitHub does not say, so each candidate is probed in
// turn, then the login; a classic token reaches every owner, so it is named by
// its login. A probe that fails for any reason but no access is retried on the
// next poll.
func findOwner(store storage.Storage, pat storage.StoredPAT, candidates []string, reach func(string) (string, error), logger *slog.Logger) {
	owner := pat.Username
	if !strings.HasPrefix(pat.Token, "ghp_") {
		for _, c := range append(candidates, pat.Username) {
			login, err := reach(c)
			if errors.Is(err, github.ErrNoAccess) {
				continue
			}
			if err != nil {
				logger.Warn("could not find the token's owner", slog.Any("error", err))
				return
			}
			owner = login
			break
		}
	}
	if err := store.SetPATOwner(pat.Token, owner); err != nil {
		logger.Warn("could not store the token's owner", slog.Any("error", err))
	}
}

func startRefreshLoop(store storage.Storage, tracker *backoff.Tracker, baseURL string, send func(notify.Notification), logger *slog.Logger) {
	announced := map[string]bool{}
	for {
		requested, ok := tracker.Next()
		if !ok {
			return
		}
		storedPat, found, _ := store.GetPAT()
		if !found {
			logger.Debug("no PAT configured, skipping refresh")
			continue
		}

		if store.IsRateLimitActive(time.Now()) {
			continue
		}

		// A timer tick (the one at startup included) skips a recent poll;
		// "poll now" and a new token do not.
		if !requested && !pollDue(store.Prs().LastFetched, time.Now(), tracker.BaseInterval()) {
			continue
		}

		prs, degradations, err := github.QueryGithub(baseURL, storedPat.Token, storedPat.Username, logger)
		if err != nil {
			var rl *github.ErrRateLimited
			if errors.As(err, &rl) {
				tracker.RateLimited()
				store.SetRateLimitUntil(rl.UnblockedAt) //nolint:errcheck // best-effort persistence
			} else if errors.Is(err, github.ErrGithubServer) {
				// A 5xx that survived the in-request retries. Github is
				// struggling with a query it usually answers, so wait a little
				// longer and try the same thing again.
				logger.Warn("could not fetch prs from github", slog.Any("error", err))
				tracker.TransientErrored()
			} else {
				// A 4xx, a network failure or a parse error: repeating it
				// sooner cannot help, so this backs off harder.
				logger.Warn("could not fetch prs from github", slog.Any("error", err))
				tracker.Errored()
			}
			continue
		}
		tracker.Succeeded()
		if storedPat.Owner == "" {
			findOwner(store, storedPat, splitOwners(*workOwners), func(owner string) (string, error) {
				return github.OwnerReach(baseURL, storedPat.Token, owner, logger)
			}, logger)
		}
		owners, err := store.OwnerPATs()
		if err != nil {
			logger.Error("could not read the owner tokens", slog.Any("error", err))
			continue
		}
		prs, ownerDs, states := mergeOwners(prs, storedPat.Username, owners, store.Prs().Prs, time.Now(),
			func(o storage.OwnerPAT) ([]types.ViewPr, []types.Degradation, error) {
				return github.PollOwner(baseURL, o.Token, storedPat.Username, o.Owner, logger)
			}, logger)
		degradations = append(degradations, ownerDs...)
		// Kept even when the result is dropped below: each state only lands
		// on the token it was polled with.
		for _, st := range states {
			if err := store.SetOwnerPollState(st); err != nil {
				logger.Warn("could not store an owner token's poll state", slog.String("owner", st.Owner), slog.Any("error", err))
			}
		}
		// The token was replaced or cleared while this poll ran: its PRs
		// belong to the old token's login, so none of them is stored.
		if current, found, _ := store.GetPAT(); !found || current.Token != storedPat.Token {
			logger.Info("the token changed during the poll, dropping its result")
			continue
		}
		if current, err := store.OwnerPATs(); err != nil || !sameTokens(owners, current) {
			logger.Info("an owner token changed during the poll, dropping its result")
			continue
		}
		// Written on every successful poll, including when the slice is empty,
		// so a deficiency that has been fixed stops being reported.
		if err := store.StoreDegradations(degradations); err != nil {
			logger.Warn("could not store degradations", slog.Any("error", err))
		}
		logDegradations(degradations, announced, logger)
		if err := store.StoreRepoPrs(prs); err != nil {
			logger.Error("could not store prs", slog.Any("error", err))
			continue
		}
		if send != nil {
			notifyChanges(store, storedPat.Username, splitOwners(*workOwners), send, logger)
		}
	}
}

// logDegradations warns about each degradation on every poll it occurs in,
// except a token limitation, which is stated once at INFO per process: it
// recurs on every poll and only a different token type would change it.
func logDegradations(ds []types.Degradation, announced map[string]bool, logger *slog.Logger) {
	for _, d := range ds {
		if d.TokenLimitation() {
			if key := d.Kind + "/" + d.Owner; !announced[key] {
				announced[key] = true
				logger.Info("names of failing Actions jobs are unavailable with this token type",
					slog.String("kind", d.Kind), slog.String("owner", d.Owner), slog.String("remedy", d.Remedy))
			}
			continue
		}
		logger.Warn("degraded", slog.String("kind", d.Kind), slog.String("owner", d.Owner),
			slog.String("message", d.Message), slog.String("remedy", d.Remedy))
	}
}

// notifyChanges sends one notification per PR that gained a reason since the
// previous fetch. The new state is stored before anything is sent: if it
// cannot be stored, sending would repeat the same notifications every fetch.
func notifyChanges(store storage.Storage, username string, workOwners []string, send func(notify.Notification), logger *slog.Logger) {
	ns, seen := notify.Diff(store.NotifySeen(), store.Prs().Prs, username, workOwners)
	if err := store.StoreNotifySeen(seen); err != nil {
		logger.Warn("could not store notify state, sending nothing", slog.Any("error", err))
		return
	}
	pos := points.Rank(store.Prs().Prs, username, time.Now())
	for _, n := range notify.Coalesce(ns, func(prUrl string) int { return pos[prUrl] }) {
		send(n)
	}
}

// pageAddress is where notifications send the user: -page-url, else the
// listen address.
func pageAddress(page, listen string) string {
	if page != "" {
		return page
	}
	return "http://" + listen
}

// splitOwners splits a comma-separated flag value, dropping empty entries.
func splitOwners(s string) []string {
	var owners []string
	for _, o := range strings.Split(s, ",") {
		if o = strings.TrimSpace(o); o != "" {
			owners = append(owners, o)
		}
	}
	return owners
}

func annotationTokenPath(flagValue, runtimeDir string) string {
	if flagValue != "" {
		return flagValue
	}
	if runtimeDir == "" {
		return ""
	}
	return filepath.Join(runtimeDir, "elly", "annotation-token")
}

// record runs one poll against github and writes it, anonymised, to the next
// free poll-N.json in dir. The database is only read, for the token and the
// username; no server starts, nothing is stored and nothing is notified. It
// refuses -verbose, because debug logging prints whole response bodies.
func record(dir string, owners []string, verbose bool, dbPath, baseURL string, logger *slog.Logger) (string, error) {
	if verbose {
		return "", errors.New("-record refuses -verbose: debug logging prints response bodies")
	}
	if len(owners) == 0 {
		return "", errors.New("-record needs -record-owners: the search reaches every org the token can see")
	}
	pat, found, err := storage.ReadPAT(dbPath)
	if err != nil {
		return "", fmt.Errorf("could not read the token: %w", err)
	}
	if !found {
		return "", errors.New("no token stored; set one up in elly first")
	}
	out, err := github.Record(baseURL, pat.Token, pat.Username, owners, logger)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	for n := 1; ; n++ {
		path := filepath.Join(dir, fmt.Sprintf("poll-%d.json", n))
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		if _, err := f.Write(out); err != nil {
			_ = f.Close()
			return "", err
		}
		return path, f.Close()
	}
}

// githubAPI checks -github-api: GitHub's API, or an http(s) URL on a
// loopback address, where a development stub listens. Anything else would
// hand the stored token to whoever answers there.
func githubAPI(raw string) (string, error) {
	raw = strings.TrimSuffix(raw, "/")
	if raw == github.DefaultAPIURL {
		return raw, nil
	}
	u, err := neturl.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("-github-api: %w", err)
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	if (u.Scheme != "http" && u.Scheme != "https") || (host != "localhost" && (ip == nil || !ip.IsLoopback())) {
		return "", fmt.Errorf("-github-api %q: only %s or an http(s) URL on a loopback address", raw, github.DefaultAPIURL)
	}
	return raw, nil
}
