package storage

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chelmertz/elly/internal/types"
	_ "github.com/mattn/go-sqlite3"
)

func check(err error) {
	if err != nil {
		panic(err)
	}
}

type Storage struct {
	dirname  string
	filename string
	sync.Mutex
	db *Queries
}

type StoredState struct {
	Prs         []types.ViewPr
	LastFetched time.Time
}

//go:embed schema.sql
var ddl string

func NewStorage() *Storage {
	s := &Storage{}
	dirname, err := os.UserCacheDir()
	check(err)
	s.dirname = filepath.Join(dirname, "elly")
	if err := os.Mkdir(s.dirname, 0770); err != nil && !errors.Is(err, os.ErrExist) {
		check(err)
	}

	s.filename = filepath.Join(s.dirname, "prs.json")

	db, err := sql.Open("sqlite3", ":memory:")
	check(err)

	// create tables
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, ddl); err != nil {
		check(err)
	}

	s.db = New(db)

	return s
}

func (s *Storage) Prs() StoredState {
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

	var prs_ = StoredState{}
	err = json.Unmarshal(oldContents, &prs_)
	check(err)

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
		})
	}
	prs_.Prs = prs

	return prs_
}

func (s *Storage) StoreRepoPrs(orderedPrs []types.ViewPr, logger *slog.Logger) error {
	s.Lock()
	defer s.Unlock()
	prs_ := StoredState{}
	prs_.Prs = make([]types.ViewPr, len(orderedPrs))
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

	return nil
}
