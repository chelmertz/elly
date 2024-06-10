package storage

import (
	"context"
	_ "embed"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/chelmertz/elly/internal/types"
	"github.com/ncruces/go-sqlite3"
	_ "github.com/ncruces/go-sqlite3/embed"
)

func check(err error) {
	if err != nil {
		panic(err)
	}
}

type Storage struct {
	db     *Queries
	logger *slog.Logger
}

type StoredState struct {
	Prs         []types.ViewPr
	LastFetched time.Time
}

//go:embed schema.sql
var ddl string

func NewStorage(logger *slog.Logger) *Storage {
	dbFilename := ":memory:"

	db, err := sqlite3.Open(fmt.Sprintf("file:%s?mode=rwc&_journal_mode=WAL&_synchronous=NORMAL", dbFilename))
	check(err)

	// create tables
	if err := db.Exec(ddl); err != nil {
		check(err)
	}

	return &Storage{
		db:     New(db),
		logger: logger,
	}
}

func (s *Storage) Prs() StoredState {
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
			UnrespondedThreads:       int(dbPr.UnrespondedThreads),
			Additions:                int(dbPr.Additions),
			Deletions:                int(dbPr.Deletions),
			ReviewRequestedFromUsers: strings.Split(dbPr.ReviewRequestedFromUsers, ","),
			Buried:                   dbPr.Buried,
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

func (s *Storage) StoreRepoPrs(orderedPrs []types.ViewPr) error {
	s.logger.Info("storing prs", slog.Int("prs", len(orderedPrs)))

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
			UnrespondedThreads:       int64(pr.UnrespondedThreads),
			Additions:                int64(pr.Additions),
			Deletions:                int64(pr.Deletions),
			ReviewRequestedFromUsers: strings.Join(pr.ReviewRequestedFromUsers, ","),
			Buried:                   false,
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

func (s *Storage) Bury(prUrl string) error {
	return s.db.Bury(context.Background(), prUrl)
}

func (s *Storage) Unbury(prUrl string) error {
	return s.db.Unbury(context.Background(), prUrl)
}

func (s *Storage) GetPr(prUrl string) (Pr, error) {
	return s.db.GetPr(context.Background(), prUrl)
}
