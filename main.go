package main

import (
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"sort"
	"strconv"
	"sync"
	"time"

	"golang.org/x/exp/slog"
)

func check(err error) {
	if err != nil {
		panic(err)
	}
}

// TODO debug level as flag
// TODO update to go version, remove experimental slog dependency

var timeoutMinutes = flag.Int("timeout", 10, "refresh PRs every N minutes")
var url = flag.String("url", "localhost:9876", "URL for web GUI")
var logger = slog.New(slog.NewTextHandler(os.Stdout))
var githubUsernameRegex = regexp.MustCompile("[a-zA-Z0-9-]+")

func main() {
	flag.Parse()

	token := os.Getenv("GITHUB_PAT")
	if token == "" {
		logger.Error("missing GITHUB_PAT env var", nil)
		os.Exit(1)
	}
	os.Unsetenv("GITHUB_PAT")

	username := os.Getenv("GITHUB_USER")
	if token == "" {
		logger.Warn("missing GITHUB_USER env var, will not assign points properly", nil)
	}
	os.Unsetenv("GITHUB_USER")

	if !githubUsernameRegex.Match([]byte(username)) {
		logger.Error("GITHUB_USER env var is not a valid github username", nil)
		os.Exit(1)
	}

	version := "unknown"
	if bi, ok := debug.ReadBuildInfo(); ok {
		version = bi.Main.Version
	}

	logger.Info("starting elly",
		slog.String("github_user", username),
		slog.String("version", version),
		slog.Int("timeout_minutes", *timeoutMinutes))

	storage := NewStorage()
	refreshChannel := StartRefreshLoop(token, username, storage)
	ServeWeb(*url, username, token, storage, refreshChannel)
}

type refreshAction string

const (
	upstart refreshAction = "upstart"
	stop    refreshAction = "stop"
	tick    refreshAction = "tick"
	manual  refreshAction = "manual"
)

func StartRefreshLoop(token, username string, storage *storage) chan refreshAction {
	refreshTimer := time.NewTicker(time.Duration(*timeoutMinutes) * time.Minute)
	refresh := make(chan refreshAction, 1)
	retriesLeft := 5

	go func() {
		for {
			select {
			case action := <-refresh:
				logger.Info("refresh loop", slog.Any("action", action))
				switch action {
				case stop:
					refreshTimer.Stop()
					return
				}
				_, err := possiblyRefreshPrs(token, username, storage)
				if err != nil {
					if errors.Is(err, errClient) {
						refreshTimer.Stop()
						logger.Error("client error when querying github, giving up", err)
						return
					} else if errors.Is(err, errGithubServer) {
						retriesLeft--
						if retriesLeft <= 0 {
							refreshTimer.Stop()
							logger.Error("too many failed github requests, giving up", nil)
							return
						}
						logger.Warn("error refreshing PRs", err, slog.Int("retries_left", retriesLeft))
					}
				}

			case <-refreshTimer.C:
				refresh <- tick
			}
		}
	}()

	refresh <- upstart
	return refresh
}

type storage struct {
	dirname  string
	filename string
	sync.Mutex
}

type prs struct {
	Prs         []ViewPr
	LastFetched time.Time
}

func NewStorage() *storage {
	s := &storage{}
	dirname, err := os.UserCacheDir()
	check(err)
	s.dirname = filepath.Join(dirname, "elly")
	if err := os.Mkdir(s.dirname, 0770); err != nil && !errors.Is(err, os.ErrExist) {
		check(err)
	}

	s.filename = filepath.Join(s.dirname, "prs.json")

	return s
}

func (s *storage) Prs() prs {
	s.Lock()
	defer s.Unlock()

	oldContents, err := os.ReadFile(s.filename)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			oldContents = []byte("{}")
		} else {
			check(err)
		}
	}

	var prs_ = prs{}
	err = json.Unmarshal(oldContents, &prs_)
	check(err)

	return prs_
}

func (s *storage) StoreRepoPrs(orderedPrs []ViewPr) error {
	s.Lock()
	defer s.Unlock()
	prs_ := prs{}
	prs_.Prs = make([]ViewPr, len(orderedPrs))
	copy(prs_.Prs, orderedPrs)
	prs_.LastFetched = time.Now()

	logger.Info("storing prs", slog.Int("prs", len(prs_.Prs)))

	newContents, err := json.Marshal(prs_)
	if err != nil {
		return fmt.Errorf("could not marshal json: %w", err)
	}

	if err := os.WriteFile(s.filename, newContents, 0660); err != nil {
		return fmt.Errorf("could not write file: %w", err)
	}

	return nil
}

type IndexHtmlData struct {
	Prs            []ViewPr
	PointsPerPrUrl map[string]*Points
	CurrentUser    string
	RefreshUrl     string
	LastRefreshed  string
}

// ViewPr must contain everything needed to order/compare them against other PRs,
// since ViewPr is also what we store.
type ViewPr struct {
	ReviewStatus             string
	Url                      string
	Title                    string
	Author                   string
	RepoName                 string
	RepoOwner                string
	RepoUrl                  string
	IsDraft                  bool
	LastUpdated              time.Time
	LastPrCommenter          string
	UnrespondedThreads       int
	Additions                int
	Deletions                int
	ReviewRequestedFromUsers []string
}

//go:embed templates/index.html
var index embed.FS

func ServeWeb(url, username, token string, storage *storage, refreshingChannel chan refreshAction) {
	temp, err := template.ParseFS(index, "templates/index.html")
	check(err)

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		storedPrs := storage.Prs()
		prs_ := storedPrs.Prs

		pointsPerPrUrl := make(map[string]*Points)
		for _, pr := range prs_ {
			pointsPerPrUrl[pr.Url] = standardPrPoints(pr, username)
		}

		sort.Slice(prs_, func(i, j int) bool {
			pri := pointsPerPrUrl[prs_[i].Url].Total
			prj := pointsPerPrUrl[prs_[j].Url].Total
			if pri == prj {
				lastUpdated := prs_[i].LastUpdated.Before(prs_[j].LastUpdated)
				return lastUpdated
			}
			return pri > prj
		})
		data := IndexHtmlData{
			Prs:            prs_,
			PointsPerPrUrl: pointsPerPrUrl,
			CurrentUser:    username,
			LastRefreshed:  storedPrs.LastFetched.Format(time.RFC3339),
		}
		err := temp.Execute(w, data)
		check(err)
	})

	// Let's say that v0 represents "may change at any time", read the code.
	// Should be bumped before tagging this repo as v1
	http.HandleFunc("/api/v0/prs", func(w http.ResponseWriter, r *http.Request) {
		storedPrs := storage.Prs().Prs
		prsToReturn := make([]ViewPr, 0)

		minimumPoints := -999
		if minPoints := r.URL.Query().Get("minPoints"); minPoints != "" {
			if min, err := strconv.Atoi(minPoints); err == nil && min >= -999 && min <= 999 {
				minimumPoints = min
			}
		}

		pointsPerPrUrl := make(map[string]*Points)
		for _, pr := range storedPrs {
			points := standardPrPoints(pr, username)
			pointsPerPrUrl[pr.Url] = points
		}

		for _, pr := range storedPrs {
			points := pointsPerPrUrl[pr.Url]
			if points.Total >= minimumPoints {
				prsToReturn = append(prsToReturn, pr)
			}
		}

		sort.Slice(prsToReturn, func(i, j int) bool {
			pri := pointsPerPrUrl[storedPrs[i].Url].Total
			prj := pointsPerPrUrl[storedPrs[j].Url].Total
			if pri == prj {
				lastUpdated := storedPrs[i].LastUpdated.Before(storedPrs[j].LastUpdated)
				return lastUpdated
			}
			return pri > prj
		})

		w.Header().Set("Content-Type", "application/json")
		err := json.NewEncoder(w).Encode(prsToReturn)
		check(err)
	})

	http.HandleFunc("/api/v0/prs/refresh", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			refreshingChannel <- manual
			w.WriteHeader(http.StatusOK)
			return
		} else if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(struct{ LastFetched time.Time }{storage.Prs().LastFetched})
			return
		} else {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
	})

	logger.Info("starting web server at", slog.String("url", "http://"+url))
	err = http.ListenAndServe(url, nil)
	check(err)
}
