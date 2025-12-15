package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"time"

	"log/slog"

	"github.com/chelmertz/elly/internal/github"
	"github.com/chelmertz/elly/internal/server"
	"github.com/chelmertz/elly/internal/storage"
	"github.com/chelmertz/elly/internal/types"
)

var timeoutMinutes = flag.Int("timeout", 5, "refresh PRs every N minutes")
var url = flag.String("url", "localhost:9876", "URL for web GUI")
var dbPath = flag.String("db", "", "path to SQLite database file (required)")
var golden = flag.Bool("golden", false, "provide a button for turning a PR into a test. do NOT use outside of development")
var demo = flag.Bool("demo", false, "mock the PRs so you can take a proper screenshot of the GUI")
var versionFlag = flag.Bool("version", false, "show version")
var verboseFlag = flag.Bool("verbose", false, "verbose logging")
var logLevel = &slog.LevelVar{}
var logger = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{AddSource: true, Level: logLevel}))

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

	if *verboseFlag {
		logLevel.Set(slog.LevelDebug)
	} else {
		logLevel.Set(slog.LevelInfo)
	}

	if *dbPath == "" {
		logger.Error("missing required -db flag")
		os.Exit(1)
	}
	dbDir := filepath.Dir(*dbPath)
	if info, err := os.Stat(dbDir); err != nil {
		logger.Error("database directory does not exist", "dir", dbDir, "error", err)
		os.Exit(1)
	} else if !info.IsDir() {
		logger.Error("database path parent is not a directory", "path", dbDir)
		os.Exit(1)
	}

	var store storage.Storage
	if *demo {
		store = storage.NewStorageDemo()
	} else {
		store = storage.NewStorage(logger, *dbPath)
	}

	setupMode, err := initPAT(store)
	if err != nil {
		logger.Error("failed to initialize PAT", slog.Any("error", err))
		os.Exit(1)
	}

	refreshChannel := make(chan types.RefreshAction, 1)

	if setupMode {
		logger.Info("starting elly in setup mode", "version", version)
	} else {
		logger.Info("starting elly", "version", version, "timeout_minutes", *timeoutMinutes, "golden_testing_enabled", *golden, "demo", *demo)
	}

	go startRefreshLoop(store, refreshChannel)
	refreshChannel <- types.RefreshUpstart

	server.ServeWeb(server.HttpServerConfig{
		Url:                  *url,
		GoldenTestingEnabled: *golden,
		Store:                store,
		RefreshingChannel:    refreshChannel,
		TimeoutMinutes:       *timeoutMinutes,
		Version:              version,
		Logger:               logger,
		SetupMode:            setupMode,
	})
}

// initPAT initializes the PAT from env var or storage.
// Returns (setupMode, error) where setupMode=true means no valid PAT is configured.
func initPAT(store storage.Storage) (bool, error) {
	envToken := os.Getenv("GITHUB_PAT")
	if envToken != "" {
		os.Unsetenv("GITHUB_PAT") //nolint:errcheck // best-effort security cleanup
		username, expiresAt, err := github.ValidatePAT(envToken, logger)
		if err != nil {
			logger.Warn("GITHUB_PAT env var is invalid, ignoring", slog.Any("error", err))
			return true, nil
		}
		if err := store.StorePAT(envToken, username, expiresAt); err != nil {
			return false, fmt.Errorf("could not store PAT from env var: %w", err)
		}
		return false, nil
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
	if _, _, err := github.ValidatePAT(storedPat.Token, logger); err != nil {
		logger.Warn("stored PAT is no longer valid, starting in setup mode", slog.Any("error", err))
		if clearErr := store.ClearPAT(); clearErr != nil {
			logger.Warn("could not clear invalid PAT", slog.Any("error", clearErr))
		}
		return true, nil
	}

	return false, nil
}

func startRefreshLoop(store storage.Storage, refresh chan types.RefreshAction) {
	refreshTimer := time.NewTicker(time.Duration(*timeoutMinutes) * time.Minute)
	retriesLeft := 5

	for {
		select {
		case action := <-refresh:
			logger.Debug("refresh loop", "action", action)
			switch action {
			case types.RefreshStop:
				refreshTimer.Stop()
				return
			}

			// Get PAT from store - skip if not configured
			storedPat, found, _ := store.GetPAT()
			if !found {
				logger.Debug("no PAT configured, skipping refresh")
				continue
			}

			if store.IsRateLimitActive(time.Now()) {
				continue
			}

			if time.Since(store.Prs().LastFetched) < time.Duration(1)*time.Minute {
				// querying github once a minute should be fine,
				// especially as long as we do the passive, loopy thing more seldom
				continue
			}

			prs, err := github.QueryGithub(storedPat.Token, storedPat.Username, logger)
			if err != nil {
				var rateLimitedError *github.ErrRateLimited
				if errors.As(err, &rateLimitedError) {
					if err := store.SetRateLimitUntil(rateLimitedError.UnblockedAt); err != nil {
						logger.Error("could not store rate limit time, exiting",
							slog.Any("error", err),
							slog.Time("rate_limit_until", rateLimitedError.UnblockedAt))
						os.Exit(1)
					}
					continue
				} else if errors.Is(err, github.ErrClient) {
					refreshTimer.Stop()
					logger.Error("client error when querying github, giving up", slog.Any("error", err))
					return
				} else if errors.Is(err, github.ErrGithubServer) {
					retriesLeft--
					if retriesLeft <= 0 {
						refreshTimer.Stop()
						logger.Error("too many failed github requests, giving up")
						return
					}
					logger.Warn("error refreshing PRs", slog.Any("error", err), slog.Int("retries_left", retriesLeft))
					continue
				}
			} else if err := store.StoreRepoPrs(prs); err != nil {
				logger.Error("could not store prs", slog.Any("error", err))
				return
			}

		case <-refreshTimer.C:
			refresh <- types.RefreshTick
		}
	}
}
