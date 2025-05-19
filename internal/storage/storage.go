package storage

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chelmertz/elly/internal/types"
	_ "github.com/mattn/go-sqlite3"
)

func check(err error) {
	if err != nil {
		panic(err)
	}
}

type Storage interface {
	Prs() StoredState
	StoreRepoPrs(orderedPrs []types.ViewPr) error
	Bury(prUrl string) error
	Unbury(prUrl string) error
	GetPr(prUrl string) (Pr, error)
}

type DbStorage struct {
	db     *Queries
	logger *slog.Logger
}

var _ Storage = (*DbStorage)(nil)

type StoredState struct {
	Prs         []types.ViewPr
	LastFetched time.Time
}

//go:embed schema.sql
var ddl string

func NewStorage(logger *slog.Logger) *DbStorage {
	dirname, err := os.UserCacheDir()
	check(err)
	ourCacheDir := filepath.Join(dirname, "elly")
	if err := os.MkdirAll(ourCacheDir, 0755); err != nil && !errors.Is(err, os.ErrExist) {
		check(err)
	}
	dbFilename := filepath.Join(ourCacheDir, "elly.db")

	db, err := sql.Open("sqlite3", fmt.Sprintf("file:%s?mode=rwc&_journal_mode=WAL&_synchronous=NORMAL", dbFilename))
	check(err)

	// create tables
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, ddl); err != nil {
		// if this fails, we should drop the old file (throwing away buried data, sadly) and retry to create the tables

		// adjust the schema.sql file by adding a column, to check the behavior. I think there's a NPE or such that we hit
		check(err)
	}

	return &DbStorage{
		db:     New(db),
		logger: logger,
	}
}

func (s *DbStorage) Prs() StoredState {
	dbPrs, err := s.db.ListPrs(context.Background())
	check(err)
	prs := make([]types.ViewPr, 0)
	for _, dbPr := range dbPrs {
		lastUpdated, err := time.Parse(time.RFC3339, dbPr.LastUpdated)
		check(err)
		prs = append(prs, types.ViewPr{
			Url:                      dbPr.Url,
			ReviewStatus:             dbPr.ReviewStatus,
			Title:                    dbPr.Title,
			Author:                   dbPr.Author,
			RepoName:                 dbPr.RepoName,
			RepoOwner:                dbPr.RepoOwner,
			RepoUrl:                  dbPr.RepoUrl,
			IsDraft:                  dbPr.IsDraft,
			LastUpdated:              lastUpdated,
			LastPrCommenter:          dbPr.LastPrCommenter,
			ThreadsActionable:        int(dbPr.ThreadsActionable),
			ThreadsWaiting:           int(dbPr.ThreadsWaiting),
			Additions:                int(dbPr.Additions),
			Deletions:                int(dbPr.Deletions),
			ReviewRequestedFromUsers: strings.Split(dbPr.ReviewRequestedFromUsers, ","),
			Buried:                   dbPr.Buried,
			RawJsonResponse:          dbPr.RawJsonResponse,
		})
	}

	state := StoredState{
		Prs: prs,
	}
	if dbLastFetched, err := s.db.GetLastFetched(context.Background()); err == nil {
		if lastFetched, err := time.Parse(time.RFC3339, dbLastFetched); err == nil {
			state.LastFetched = lastFetched
		}
	}

	return state
}

func (s *DbStorage) StoreRepoPrs(orderedPrs []types.ViewPr) error {
	s.logger.Debug("storing prs", slog.Int("prs", len(orderedPrs)))

	buriedPrs, err := s.db.BuriedPrs(context.Background())
	if err != nil {
		s.logger.Error("could not fetch buried prs, throwing away old buried-status", slog.Any("err", err))
	} else {
		// O(n^2) but n is always small, in my case (and the amount of buried prs is usually super small)
		for i, pr := range orderedPrs {
			for _, buriedPr := range buriedPrs {
				if buriedPr.Url == pr.Url {
					buriedLastUpdated, err := time.Parse(time.RFC3339, buriedPr.LastUpdated)
					if err != nil {
						// just ignore the stored & old last updated time
						s.logger.Info("unburying pr, old last updated time is invalid", slog.String("pr_url", pr.Url))
						orderedPrs[i].Buried = false
						break
					}
					if pr.LastUpdated.After(buriedLastUpdated) {
						// the pr was updated since it was buried
						s.logger.Info("unburying pr, it was updated since it was buried",
							slog.String("pr_url", pr.Url),
							slog.Time("stored_at", buriedLastUpdated),
							slog.Time("new_updated_at", pr.LastUpdated),
						)
						orderedPrs[i].Buried = false
						break
					}
					orderedPrs[i].Buried = true
					break
				}
			}
		}
	}

	if err := s.db.DeletePrs(context.Background()); err != nil {
		return fmt.Errorf("could not delete old prs, in preparation of storing new ones: %w", err)
	}

	for _, pr := range orderedPrs {
		_, err := s.db.CreatePr(context.Background(), CreatePrParams{
			Url:                      pr.Url,
			ReviewStatus:             pr.ReviewStatus,
			Title:                    pr.Title,
			Author:                   pr.Author,
			RepoName:                 pr.RepoName,
			RepoOwner:                pr.RepoOwner,
			RepoUrl:                  pr.RepoUrl,
			IsDraft:                  pr.IsDraft,
			LastUpdated:              pr.LastUpdated.Format(time.RFC3339),
			LastPrCommenter:          pr.LastPrCommenter,
			ThreadsActionable:        int64(pr.ThreadsActionable),
			ThreadsWaiting:           int64(pr.ThreadsWaiting),
			Additions:                int64(pr.Additions),
			Deletions:                int64(pr.Deletions),
			ReviewRequestedFromUsers: strings.Join(pr.ReviewRequestedFromUsers, ","),
			Buried:                   pr.Buried,
			RawJsonResponse:          pr.RawJsonResponse,
		})
		check(err)
	}

	now := time.Now()
	nowFormatted := now.Format(time.RFC3339)
	if err := s.db.StoreLastFetched(context.Background(), nowFormatted); err != nil {
		return fmt.Errorf("could not store last fetched time: %w", err)
	}

	return nil
}

func (s *DbStorage) Bury(prUrl string) error {
	return s.db.Bury(context.Background(), prUrl)
}

func (s *DbStorage) Unbury(prUrl string) error {
	return s.db.Unbury(context.Background(), prUrl)
}

func (s *DbStorage) GetPr(prUrl string) (Pr, error) {
	return s.db.GetPr(context.Background(), prUrl)
}
