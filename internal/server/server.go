package server

import (
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chelmertz/elly/internal/backoff"
	"github.com/chelmertz/elly/internal/github"
	"github.com/chelmertz/elly/internal/points"
	"github.com/chelmertz/elly/internal/storage"
	"github.com/chelmertz/elly/internal/types"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func check(err error) {
	if err != nil {
		panic(err)
	}
}

// pageDegradations leaves out token limitations: a warning on every page view
// about something only a different token type fixes is noise. The API still
// reports them, so a consumer never mistakes missing check names for none.
func pageDegradations(ds []types.Degradation) []types.Degradation {
	var out []types.Degradation
	for _, d := range ds {
		if !d.TokenLimitation() {
			out = append(out, d)
		}
	}
	return out
}

// ownerLogin is GitHub's login syntax: up to 39 letters, digits and
// hyphens, not starting with a hyphen.
var ownerLogin = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`)

// readToken reads {"token": "..."} from a PAT route's body, or answers the
// request itself and returns "".
func readToken(w http.ResponseWriter, r *http.Request) string {
	var req struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPatBody)).Decode(&req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "body over 4 KiB")
			return ""
		}
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return ""
	}
	if req.Token == "" {
		writeError(w, http.StatusBadRequest, "token is required")
	}
	return req.Token
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": msg})
}

// patWriteTimeout is how long checking a pasted token may take: one
// minute per GitHub request, and validation makes several.
const patWriteTimeout = 5 * time.Minute

// maxPatBody fits any GitHub token several times over.
const maxPatBody = 4 << 10

//go:embed next.html
var index embed.FS

type HttpServerConfig struct {
	Url            string
	Store          storage.Storage
	TimeoutMinutes int
	Version        string
	Logger         *slog.Logger
	Tracker        *backoff.Tracker
	SetupMode      bool // True if no PAT configured (initial state only)
	// AnnotationToken is the bearer token annotation writes must carry. Empty
	// disables them.
	AnnotationToken string
	// AnnotationSchemes are the link schemes allowed besides https.
	AnnotationSchemes []string
	// ReviewRepos are where claude-review runs rounds, "owner/*" or
	// "owner/repo" (review_repos in its config); see buildNext.
	ReviewRepos []string
	// AllowedHosts are Host values elly answers besides loopback names on
	// the listen port (-allowed-hosts); see guard.
	AllowedHosts []string
	// GithubURL is where a pasted PAT is checked; empty means GitHub's API.
	GithubURL string
	// Changes streams page changes on GET /api/v0/events; nil leaves the
	// route out and the page falls back to its timed reload.
	Changes *Changes
}

func (c HttpServerConfig) githubURL() string {
	if c.GithubURL == "" {
		return github.DefaultAPIURL
	}
	return c.GithubURL
}

func (c HttpServerConfig) logger() *slog.Logger {
	if c.Logger == nil {
		return slog.Default()
	}
	return c.Logger
}

// getCurrentUsername returns the username from the stored PAT, or empty string if not configured.
// prsAtOrAbove returns the PRs scoring at least minimumPoints, highest first,
// most recently updated first on a tie.
func prsAtOrAbove(prs []types.ViewPr, currentUser string, minimumPoints int, now time.Time) []types.ViewPr {
	pointsPerPrUrl := make(map[string]*points.Points)
	for _, pr := range prs {
		pointsPerPrUrl[pr.Url] = points.StandardPrPoints(pr, currentUser, now)
	}

	prsToReturn := make([]types.ViewPr, 0)
	for _, pr := range prs {
		if pointsPerPrUrl[pr.Url].Total >= minimumPoints {
			prsToReturn = append(prsToReturn, pr)
		}
	}

	sort.Slice(prsToReturn, func(i, j int) bool {
		pri := pointsPerPrUrl[prsToReturn[i].Url].Total
		prj := pointsPerPrUrl[prsToReturn[j].Url].Total
		if pri == prj {
			return prsToReturn[j].LastUpdated.Before(prsToReturn[i].LastUpdated)
		}
		return pri > prj
	})
	return prsToReturn
}

// prsOnUser is every PR the page puts in a group on the user, in page order.
func prsOnUser(prs []types.ViewPr, currentUser string, now time.Time) []types.ViewPr {
	ranks := points.Rank(prs, currentUser, now)
	out := make([]types.ViewPr, 0)
	for _, pr := range prs {
		if points.TurnOf(pr, currentUser, now).Group.OnYou() {
			out = append(out, pr)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return ranks[out[i].Url] < ranks[out[j].Url] })
	return out
}

func getCurrentUsername(store storage.Storage) string {
	storedPat, found, _ := store.GetPAT()
	if !found {
		return ""
	}
	return storedPat.Username
}

// parseNext parses the page. PR titles, authors and repo names are written by
// whoever opens a PR that involves the user, so html/template escapes them;
// every link is checked in Go before it reaches the template (see buildNext).
func parseNext() (*template.Template, error) {
	return template.New("next.html").ParseFS(index, "next.html")
}

func ServeWeb(webConfig HttpServerConfig) {
	mux := routes(webConfig)
	webConfig.Logger.Info("starting web server at", slog.String("url", "http://"+webConfig.Url))
	check(newHTTPServer(webConfig.Url, guard(webConfig.Url, webConfig.AllowedHosts, mux), defaultTimeouts).ListenAndServe())
}

type serverTimeouts struct{ header, read, write, idle time.Duration }

// defaultTimeouts keep a slow or silent client from holding a connection.
// Checking a pasted token takes longer than write; its route extends its
// own deadline (patWriteTimeout).
var defaultTimeouts = serverTimeouts{header: 5 * time.Second, read: 15 * time.Second, write: 30 * time.Second, idle: time.Minute}

func newHTTPServer(addr string, handler http.Handler, t serverTimeouts) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: t.header,
		ReadTimeout:       t.read,
		WriteTimeout:      t.write,
		IdleTimeout:       t.idle,
	}
}

// decodePrId reads a PR id from a path: base64 of the PR URL, in the URL-safe
// alphabet the page's links use (types.ViewPr.Id) or the standard one
// claude-review sends (annotationURL in its elly.go).
func decodePrId(id string) (string, error) {
	b, err := base64.URLEncoding.DecodeString(id)
	if err != nil {
		b, err = base64.StdEncoding.DecodeString(id)
	}
	return string(b), err
}

// routes registers every handler on a mux of its own, so tests can build it.
func routes(webConfig HttpServerConfig) *http.ServeMux {
	mux := http.NewServeMux()
	nextTemp, err := parseNext()
	check(err)

	registerAnnotationRoutes(mux, webConfig)
	if webConfig.Changes != nil {
		mux.HandleFunc("GET /api/v0/events", webConfig.Changes.serveEvents)
	}

	mux.HandleFunc("POST /api/v0/prs/{prUrl}/{action}", func(w http.ResponseWriter, r *http.Request) {
		ghPrUrl, err := decodePrId(r.PathValue("prUrl"))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, "invalid PR ID") //nolint:errcheck // best-effort response body
			return
		}

		action := r.PathValue("action")

		var buryFunc func(string) error
		switch action {
		case "bury":
			buryFunc = webConfig.Store.Bury
		case "unbury":
			buryFunc = webConfig.Store.Unbury
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprintf(w, "action '%s' is not supported", action) //nolint:errcheck // best-effort response body
			return
		}

		if err := buryFunc(ghPrUrl); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, "couldn't toggle bury for PR %s", ghPrUrl) //nolint:errcheck // best-effort response body
			return
		}

		w.WriteHeader(http.StatusNoContent)
	})

	// Let's say that v0 represents "may change at any time", read the code.
	// Should be bumped before tagging this repo as v1
	mux.HandleFunc("GET /api/v0/prs", func(w http.ResponseWriter, r *http.Request) {
		minimumPoints := -999
		if minPoints := r.URL.Query().Get("minPoints"); minPoints != "" {
			if min, err := strconv.Atoi(minPoints); err == nil && min >= -999 && min <= 999 {
				minimumPoints = min
			}
		}

		prsToReturn := prsAtOrAbove(webConfig.Store.Prs().Prs, getCurrentUsername(webConfig.Store), minimumPoints, time.Now())
		// turn=you keeps what the page counts as on the user, in page order
		if r.URL.Query().Get("turn") == "you" {
			prsToReturn = prsOnUser(webConfig.Store.Prs().Prs, getCurrentUsername(webConfig.Store), time.Now())
		}

		w.Header().Set("Content-Type", "application/json")
		err := json.NewEncoder(w).Encode(prsToReturn)
		check(err)
	})

	mux.HandleFunc("POST /api/v0/prs/refresh", func(w http.ResponseWriter, r *http.Request) {
		webConfig.Tracker.RequestRefresh()
	})

	// {$}: only the root; any other path is a 404, not the page
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		storedPrs := webConfig.Store.Prs()
		pat, found, _ := webConfig.Store.GetPAT()
		data := buildNext(storedPrs.Prs, getCurrentUsername(webConfig.Store), time.Now(), webConfig.AnnotationSchemes, webConfig.ReviewRepos)
		data.LastRefreshed = storedPrs.LastFetched.Format(time.RFC3339)
		data.RefreshIntervalMinutes = webConfig.TimeoutMinutes
		data.Degradations = pageDegradations(webConfig.Store.Degradations())
		data.SetupMode = !found
		data.Version = webConfig.Version
		if found {
			data.Token = tokenInfo(pat, time.Now())
			owners, err := webConfig.Store.OwnerPATs()
			if err != nil {
				webConfig.Logger.Warn("could not list owner tokens", slog.Any("error", err))
			}
			data.OwnerTokens = ownerTokenInfo(owners, time.Now())
		}
		if until := webConfig.Store.GetRateLimitUntil(); until.After(time.Now()) {
			data.RateLimitedUntil = until.Format(time.RFC3339)
		}
		check(nextTemp.Execute(w, data))
	})

	// the page lived at /next beside the old one; the browser keeps the #row
	mux.HandleFunc("GET /next", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/", http.StatusFound)
	})

	mux.Handle("GET /metrics", promhttp.Handler())

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok") //nolint:errcheck // best-effort response body
	})

	// One validation at a time: each costs GitHub requests, and a second
	// PUT while one is being checked is refused rather than queued.
	var validating sync.Mutex
	// clears counts DELETEs, under patMu, which also covers a PUT's store: a
	// clear that lands while a PUT is checking its token wins, and the PUT
	// stores nothing.
	var patMu sync.Mutex
	var clears uint64
	mux.HandleFunc("PUT /api/v0/config/pat", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		token := readToken(w, r)
		if token == "" {
			return
		}

		if !validating.TryLock() {
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "a token is already being checked, try again"})
			return
		}
		defer validating.Unlock()
		// The check can outlast the server's write timeout. A recorder in
		// tests does not support deadlines, which only loses the extension.
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(patWriteTimeout))
		patMu.Lock()
		clearsBefore := clears
		patMu.Unlock()

		// Validate the token with GitHub (checks both authentication and
		// required scopes). GitHub's text goes to the log only; the token
		// goes nowhere.
		username, expiresAt, err := github.ValidatePAT(webConfig.githubURL(), token, webConfig.logger())
		if err != nil {
			webConfig.logger().Warn("could not validate a pasted token", slog.Any("error", err))
			if errors.Is(err, github.ErrInvalidToken) {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": "GitHub refused the token"})
				return
			}
			w.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "could not reach GitHub, try again"})
			return
		}

		patMu.Lock()
		if clears != clearsBefore {
			patMu.Unlock()
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "the token was cleared while this one was checked; nothing stored"})
			return
		}
		err = webConfig.Store.StorePAT(token, username, expiresAt)
		patMu.Unlock()
		if err != nil {
			webConfig.Logger.Error("could not store PAT", slog.Any("error", err))
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "could not store token"})
			return
		}

		// Trigger a refresh so the new PAT is used immediately
		webConfig.Tracker.RequestRefresh()

		response := map[string]any{
			"username": username,
		}
		if !expiresAt.IsZero() {
			response["expires_at"] = expiresAt.Format(time.RFC3339)
		}
		_ = json.NewEncoder(w).Encode(response)
	})

	mux.HandleFunc("GET /api/v0/config/status", func(w http.ResponseWriter, r *http.Request) {
		storedPat, found, _ := webConfig.Store.GetPAT()
		if !found {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"configured": false,
			})
			return
		}

		w.Header().Set("Content-Type", "application/json")
		// Degradations are reported here rather than left to be inferred from
		// data that merely looks empty. "checks_unreadable" is the case that
		// prompted it: without the Checks read permission a red PR comes back
		// with no failing-check names, which is indistinguishable from a PR
		// with nothing failing unless elly says which it is.
		degradations := make([]map[string]string, 0)
		for _, d := range webConfig.Store.Degradations() {
			m := map[string]string{
				"kind":    d.Kind,
				"message": d.Message,
				"remedy":  d.Remedy,
				"seen":    d.Seen.Format(time.RFC3339),
			}
			if d.Owner != "" {
				m["owner"] = d.Owner
			}
			degradations = append(degradations, m)
		}
		// Never the token itself, as for the primary.
		ownerTokens := make([]map[string]string, 0)
		owners, err := webConfig.Store.OwnerPATs()
		if err != nil {
			webConfig.Logger.Warn("could not list owner tokens", slog.Any("error", err))
		}
		for _, o := range owners {
			m := map[string]string{"owner": o.Owner, "username": o.Username, "stored_at": o.SetAt.Format(time.RFC3339)}
			if !o.ExpiresAt.IsZero() {
				m["expires_at"] = o.ExpiresAt.Format(time.RFC3339)
			}
			if o.Refused != "" {
				m["refused"] = o.Refused
			}
			if !o.LastSuccess.IsZero() {
				m["last_success"] = o.LastSuccess.Format(time.RFC3339)
			}
			ownerTokens = append(ownerTokens, m)
		}
		response := map[string]any{
			"configured":   true,
			"username":     storedPat.Username,
			"stored_at":    storedPat.SetAt.Format(time.RFC3339),
			"degradations": degradations,
			"owner_tokens": ownerTokens,
		}
		if !storedPat.ExpiresAt.IsZero() {
			response["expires_at"] = storedPat.ExpiresAt.Format(time.RFC3339)
		}
		_ = json.NewEncoder(w).Encode(response)
	})

	// ownerDeletes is to an owner token what clears is to the primary: a
	// DELETE that lands while a PUT for the same owner is being checked wins.
	ownerDeletes := map[string]uint64{}
	mux.HandleFunc("PUT /api/v0/config/pat/{owner}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		owner := r.PathValue("owner")
		if !ownerLogin.MatchString(owner) {
			writeError(w, http.StatusBadRequest, "not a GitHub owner name")
			return
		}
		token := readToken(w, r)
		if token == "" {
			return
		}
		// A classic token is not bound to one owner and may hold write
		// scopes; a fine-grained one is the only kind that covers exactly
		// the owner it is stored for.
		if !strings.HasPrefix(token, "github_pat_") {
			writeError(w, http.StatusBadRequest, "a token for one owner must be fine-grained (github_pat_...)")
			return
		}
		if !validating.TryLock() {
			writeError(w, http.StatusTooManyRequests, "a token is already being checked, try again")
			return
		}
		defer validating.Unlock()
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(patWriteTimeout))
		key := strings.ToLower(owner)
		patMu.Lock()
		deletesBefore := ownerDeletes[key]
		patMu.Unlock()

		username, expiresAt, err := github.ValidatePAT(webConfig.githubURL(), token, webConfig.logger())
		if err == nil {
			owner, err = github.OwnerReach(webConfig.githubURL(), token, owner, webConfig.logger())
		}
		if err != nil {
			webConfig.logger().Warn("could not validate a pasted owner token", slog.String("owner", key), slog.Any("error", err))
			switch {
			case errors.Is(err, github.ErrInvalidToken):
				writeError(w, http.StatusBadRequest, "GitHub refused the token")
			case errors.Is(err, github.ErrNoAccess):
				writeError(w, http.StatusBadRequest, "the token sees none of "+r.PathValue("owner")+"'s private repositories: is the owner's approval still pending, or is its resource owner another account?")
			default:
				writeError(w, http.StatusBadGateway, "could not reach GitHub, try again")
			}
			return
		}

		patMu.Lock()
		if ownerDeletes[key] != deletesBefore {
			patMu.Unlock()
			writeError(w, http.StatusConflict, "the token was removed while this one was checked; nothing stored")
			return
		}
		err = webConfig.Store.PutOwnerPAT(owner, token, username, expiresAt)
		patMu.Unlock()
		switch {
		case errors.Is(err, storage.ErrNoPrimary):
			writeError(w, http.StatusConflict, "set the main token first")
			return
		case errors.Is(err, storage.ErrOtherLogin):
			writeError(w, http.StatusBadRequest, "the token belongs to another login than the main token")
			return
		case err != nil:
			webConfig.Logger.Error("could not store an owner token", slog.Any("error", err))
			writeError(w, http.StatusInternalServerError, "could not store token")
			return
		}
		webConfig.Tracker.RequestRefresh()
		response := map[string]any{"owner": owner, "username": username}
		if !expiresAt.IsZero() {
			response["expires_at"] = expiresAt.Format(time.RFC3339)
		}
		_ = json.NewEncoder(w).Encode(response)
	})

	mux.HandleFunc("DELETE /api/v0/config/pat/{owner}", func(w http.ResponseWriter, r *http.Request) {
		patMu.Lock()
		ownerDeletes[strings.ToLower(r.PathValue("owner"))]++
		found, err := webConfig.Store.DeleteOwnerPAT(r.PathValue("owner"))
		patMu.Unlock()
		switch {
		case err != nil:
			webConfig.Logger.Error("could not delete an owner token", slog.Any("error", err))
			writeError(w, http.StatusInternalServerError, "could not delete token")
		case !found:
			writeError(w, http.StatusNotFound, "no token for that owner")
		default:
			webConfig.Tracker.RequestRefresh()
			w.WriteHeader(http.StatusNoContent)
		}
	})

	mux.HandleFunc("DELETE /api/v0/config/pat", func(w http.ResponseWriter, r *http.Request) {
		patMu.Lock()
		clears++
		err := webConfig.Store.ClearPAT()
		patMu.Unlock()
		if err != nil {
			webConfig.Logger.Error("could not clear PAT", slog.Any("error", err))
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "could not clear token"})
			return
		}

		// The poll loop idles without a token rather than ending, so a
		// token set later is polled.
		w.WriteHeader(http.StatusNoContent)
	})

	return mux
}
