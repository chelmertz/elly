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

	"github.com/chelmertz/elly/internal/backoff"
	"github.com/chelmertz/elly/internal/github"
	"github.com/chelmertz/elly/internal/server"
	"github.com/chelmertz/elly/internal/storage"
)

var timeoutMinutes = flag.Int("timeout", 5, "refresh PRs every N minutes")
var url = flag.String("url", "localhost:9876", "URL for web GUI")
var dbPath = flag.String("db", "", "path to SQLite database file (default: OS-appropriate data dir)")
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
		*dbPath = defaultDBPath()
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
	}

	setupMode, err := initPAT(store)
	if err != nil {
		logger.Error("failed to initialize PAT", slog.Any("error", err))
		os.Exit(1)
	}

	if setupMode {
		logger.Info("starting elly in setup mode", "version", version, "db", *dbPath)
	} else {
		logger.Info("starting elly", "version", version, "db", *dbPath, "timeout_minutes", *timeoutMinutes, "golden_testing_enabled", *golden, "demo", *demo)
	}

	tracker := backoff.New(logger, time.Duration(*timeoutMinutes)*time.Minute)

	go startRefreshLoop(store, tracker)

	server.ServeWeb(server.HttpServerConfig{
		Url:                  *url,
		GoldenTestingEnabled: *golden,
		Store:                store,
		Tracker:              tracker,
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
	if _, _, err := github.ValidatePAT(storedPat.Token, logger); err != nil {
		logger.Warn("stored PAT is no longer valid, starting in setup mode", slog.Any("error", err))
		if clearErr := store.ClearPAT(); clearErr != nil {
			logger.Warn("could not clear invalid PAT", slog.Any("error", clearErr))
		}
		return true, nil
	}

	return false, nil
}

func startRefreshLoop(store storage.Storage, tracker *backoff.Tracker) {
	for tracker.Tick() {
		storedPat, found, _ := store.GetPAT()
		if !found {
			logger.Debug("no PAT configured, skipping refresh")
			continue
		}

		if store.IsRateLimitActive(time.Now()) {
			continue
		}

		if time.Since(store.Prs().LastFetched) < tracker.BaseInterval() {
			continue
		}

		prs, err := github.QueryGithub(storedPat.Token, storedPat.Username, logger)
		if err != nil {
			var rl *github.ErrRateLimited
			if errors.As(err, &rl) {
				tracker.RateLimited()
				store.SetRateLimitUntil(rl.UnblockedAt) //nolint:errcheck // best-effort persistence
			} else if errors.Is(err, github.ErrClient) {
				logger.Error("client error, giving up", "error", err)
				tracker.Stop()
				return
			} else if errors.Is(err, github.ErrGithubServer) {
				tracker.ServerErrored()
			}
			continue
		}
		tracker.Succeeded()
		if err := store.StoreRepoPrs(prs); err != nil {
			logger.Error("could not store prs", slog.Any("error", err))
		}
	}
}
