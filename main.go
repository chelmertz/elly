package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"runtime/debug"
	"time"

	"log/slog"

	"github.com/chelmertz/elly/internal/github"
	"github.com/chelmertz/elly/internal/server"
	"github.com/chelmertz/elly/internal/storage"
	"github.com/chelmertz/elly/internal/types"
)

var timeoutMinutes = flag.Int("timeout", 10, "refresh PRs every N minutes")
var url = flag.String("url", "localhost:9876", "URL for web GUI")
var versionFlag = flag.Bool("version", false, "show version")
var logger = slog.New(slog.NewTextHandler(os.Stdout, nil))

func main() {
	flag.Parse()

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

	// TODO try out with bad github pat and make sure it fails gracefully (and is shown in GUI)
	token := os.Getenv("GITHUB_PAT")
	if token == "" {
		logger.Error("missing GITHUB_PAT env var")
		os.Exit(1)
	}
	os.Unsetenv("GITHUB_PAT")

	logger.Info("starting elly",
		slog.String("version", version),
		slog.Int("timeout_minutes", *timeoutMinutes))

	store := storage.NewStorage(logger)
	username, err := github.UsernameFromPat(token, logger)
	if err != nil {
		logger.Error("could not get username from PAT", slog.Any("error", err))
		os.Exit(1)
	}
	logger.Info("github user fetched from token", slog.String("github_user", username))

	refreshChannel := StartRefreshLoop(token, username, store)
	server.ServeWeb(*url, username, store, refreshChannel, *timeoutMinutes, version, logger)
}

func StartRefreshLoop(token, username string, store *storage.Storage) chan types.RefreshAction {
	refreshTimer := time.NewTicker(time.Duration(*timeoutMinutes) * time.Minute)
	refresh := make(chan types.RefreshAction, 1)
	retriesLeft := 5

	go func() {
		for {
			select {
			case action := <-refresh:
				logger.Info("refresh loop", slog.Any("action", action))
				switch action {
				case types.RefreshStop:
					refreshTimer.Stop()
					return
				}

				if time.Since(store.Prs().LastFetched) < time.Duration(1)*time.Minute {
					// querying github once a minute should be fine,
					// especially as long as we do the passive, loopy thing more seldom
					continue
				}

				prs, err := github.QueryGithub(token, username, logger)
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
						return
					}
				} else if err := store.StoreRepoPrs(prs); err != nil {
					logger.Error("could not store prs", slog.Any("error", err))
					return
				}

			case <-refreshTimer.C:
				refresh <- types.RefreshTick
			}
		}
	}()

	refresh <- types.RefreshUpstart
	return refresh
}
