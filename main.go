package main

import (
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"html/template"
	"net/http"
	"os"
	"regexp"
	"runtime/debug"
	"sort"
	"strconv"
	"time"

	"log/slog"

	"github.com/chelmertz/elly/internal/github"
	"github.com/chelmertz/elly/internal/points"
	"github.com/chelmertz/elly/internal/storage"
	"github.com/chelmertz/elly/internal/types"
)

func check(err error) {
	if err != nil {
		panic(err)
	}
}

var timeoutMinutes = flag.Int("timeout", 10, "refresh PRs every N minutes")
var url = flag.String("url", "localhost:9876", "URL for web GUI")
var logger = slog.New(slog.NewTextHandler(os.Stdout, nil))
var githubUsernameRegex = regexp.MustCompile("[a-zA-Z0-9-]+")

func main() {
	flag.Parse()

	// TODO try out with bad github pat and make sure it fails gracefully (and is shown in GUI)
	token := os.Getenv("GITHUB_PAT")
	if token == "" {
		logger.Error("missing GITHUB_PAT env var")
		os.Exit(1)
	}
	os.Unsetenv("GITHUB_PAT")

	username := os.Getenv("GITHUB_USER")
	if token == "" {
		logger.Warn("missing GITHUB_USER env var, will not assign points properly")
	}
	os.Unsetenv("GITHUB_USER")

	if !githubUsernameRegex.Match([]byte(username)) {
		logger.Error("GITHUB_USER env var is not a valid github username")
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

	store := storage.NewStorage()
	refreshChannel := StartRefreshLoop(token, username, store)
	ServeWeb(*url, username, token, store, refreshChannel)
}

type refreshAction string

const (
	upstart refreshAction = "upstart"
	stop    refreshAction = "stop"
	tick    refreshAction = "tick"
	manual  refreshAction = "manual"
)

func StartRefreshLoop(token, username string, store *storage.Storage) chan refreshAction {
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
				_, err := github.PossiblyRefreshPrs(token, username, store, logger)
				if err != nil {
					if errors.Is(err, github.ErrClient) {
						refreshTimer.Stop()
						logger.Error("client error when querying github, giving up", err)
						return
					} else if errors.Is(err, github.ErrGithubServer) {
						retriesLeft--
						if retriesLeft <= 0 {
							refreshTimer.Stop()
							logger.Error("too many failed github requests, giving up")
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

type IndexHtmlData struct {
	Prs            []types.ViewPr
	PointsPerPrUrl map[string]*points.Points
	CurrentUser    string
	RefreshUrl     string
	LastRefreshed  string
}

//go:embed templates/index.html
var index embed.FS

func ServeWeb(url, username, token string, store *storage.Storage, refreshingChannel chan refreshAction) {
	temp, err := template.ParseFS(index, "templates/index.html")
	check(err)

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		storedPrs := store.Prs()
		prs_ := storedPrs.Prs

		pointsPerPrUrl := make(map[string]*points.Points)
		for _, pr := range prs_ {
			pointsPerPrUrl[pr.Url] = points.StandardPrPoints(pr, username)
		}

		sort.Slice(prs_, func(i, j int) bool {
			pri := pointsPerPrUrl[prs_[i].Url].Total
			prj := pointsPerPrUrl[prs_[j].Url].Total
			if pri == prj {
				lastUpdated := prs_[j].LastUpdated.Before(prs_[i].LastUpdated)
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
		storedPrs := store.Prs().Prs
		prsToReturn := make([]types.ViewPr, 0)

		minimumPoints := -999
		if minPoints := r.URL.Query().Get("minPoints"); minPoints != "" {
			if min, err := strconv.Atoi(minPoints); err == nil && min >= -999 && min <= 999 {
				minimumPoints = min
			}
		}

		pointsPerPrUrl := make(map[string]*points.Points)
		for _, pr := range storedPrs {
			points := points.StandardPrPoints(pr, username)
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
				lastUpdated := storedPrs[j].LastUpdated.Before(storedPrs[i].LastUpdated)
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
		} else if r.Method == http.MethodGet {
		} else {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
	})

	logger.Info("starting web server at", slog.String("url", "http://"+url))
	err = http.ListenAndServe(url, nil)
	check(err)
}
