package storage

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/chelmertz/elly/internal/types"
	_ "modernc.org/sqlite"
)

func check(err error) {
	if err != nil {
		panic(err)
	}
}

// StoredPAT represents a stored PAT with metadata.
type StoredPAT struct {
	Token     string
	Username  string
	SetAt     time.Time
	ExpiresAt time.Time // Zero time if non-expiring
}

type Storage interface {
	Prs() StoredState
	StoreRepoPrs(orderedPrs []types.ViewPr) error
	Bury(prUrl string) error
	Unbury(prUrl string) error
	GetPr(prUrl string) (Pr, error)
	// SetRateLimitUntil stores the rate limit expiry time.
	SetRateLimitUntil(t time.Time) error
	// IsRateLimitActive returns true if a rate limit is in effect (caller should skip querying).
	// If the rate limit has expired, it is automatically cleared and false is returned.
	IsRateLimitActive(now time.Time) bool
	// GetRateLimitUntil returns the rate limit expiry time, or zero time if not rate limited.
	GetRateLimitUntil() time.Time
	// StorePAT stores a new PAT, deactivating any existing active PAT.
	StorePAT(token, username string, expiresAt time.Time) error
	// GetPAT returns the active PAT. Returns (pat, true, nil) if found,
	// (zero, false, nil) if not configured, or (zero, false, err) on error.
	GetPAT() (StoredPAT, bool, error)
	// ClearPAT deactivates the active PAT.
	ClearPAT() error
}

type DbStorage struct {
	db     *Queries
	rawDb  *sql.DB
	logger *slog.Logger
}

var _ Storage = (*DbStorage)(nil)

type StoredState struct {
	Prs         []types.ViewPr
	LastFetched time.Time
}

//go:embed schema.sql
var ddl string

func NewStorage(logger *slog.Logger, dbPath string) *DbStorage {
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?mode=rwc&_journal_mode=WAL&_synchronous=NORMAL", dbPath))
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
		rawDb:  db,
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

func (s *DbStorage) SetRateLimitUntil(t time.Time) error {
	hoursLeft := time.Until(t).Hours()
	s.logger.Warn("rate limited", slog.Time("will_unblock_at", t), slog.Float64("hours_left", hoursLeft))
	return s.db.StoreRateLimitUntil(context.Background(), t.Format(time.RFC3339))
}

func (s *DbStorage) IsRateLimitActive(now time.Time) bool {
	val, err := s.db.GetRateLimitUntil(context.Background())
	if errors.Is(err, sql.ErrNoRows) {
		// "clear rate limit" deletes row, so this means we're not rate limited
		return false
	}
	if err != nil {
		s.logger.Error("could not read stored rate limit time, assume rate limited (worst case)", slog.Any("error", err))
		return true
	}
	rateLimitUntil, err := time.Parse(time.RFC3339, val)
	if err != nil {
		s.logger.Error("could not parse stored rate limit time, assume rate limited (worst case). requires you to modify the database manually", slog.Any("error", err), slog.String("rate_limit_until", val))
		return true
	}
	if now.Before(rateLimitUntil) {
		return true
	}

	// rate limit expired, clear it (ignore errors - stale value will be checked again next tick)
	_ = s.db.ClearRateLimitUntil(context.Background())
	s.logger.Info("no longer rate limited")
	return false
}

func (s *DbStorage) GetRateLimitUntil() time.Time {
	val, err := s.db.GetRateLimitUntil(context.Background())
	if err != nil {
		return time.Time{}
	}
	rateLimitUntil, err := time.Parse(time.RFC3339, val)
	if err != nil {
		return time.Time{}
	}
	return rateLimitUntil
}

func (s *DbStorage) StorePAT(token, username string, expiresAt time.Time) error {
	ctx := context.Background()
	tx, err := s.rawDb.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("could not begin PAT transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback() // no-op if committed
	}()

	qtx := s.db.WithTx(tx)

	if err := qtx.DeactivateAllPATs(ctx); err != nil {
		return fmt.Errorf("could not deactivate existing PATs: %w", err)
	}

	expiresAtStr := ""
	if !expiresAt.IsZero() {
		expiresAtStr = expiresAt.Format(time.RFC3339)
	}

	if err := qtx.InsertPAT(ctx, InsertPATParams{
		Pat:       token,
		ExpiresAt: expiresAtStr,
		Username:  username,
	}); err != nil {
		return fmt.Errorf("could not insert PAT: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("could not commit PAT transaction: %w", err)
	}

	return nil
}

func (s *DbStorage) GetPAT() (StoredPAT, bool, error) {
	row, err := s.db.GetActivePAT(context.Background())
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return StoredPAT{}, false, nil
		}
		return StoredPAT{}, false, fmt.Errorf("could not get PAT: %w", err)
	}

	setAt, err := time.Parse(time.RFC3339, row.SetAt)
	if err != nil {
		return StoredPAT{}, false, fmt.Errorf("could not parse set_at: %w", err)
	}

	var expiresAt time.Time
	if row.ExpiresAt != "" {
		expiresAt, err = time.Parse(time.RFC3339, row.ExpiresAt)
		if err != nil {
			return StoredPAT{}, false, fmt.Errorf("could not parse expires_at: %w", err)
		}
	}

	return StoredPAT{
		Token:     row.Pat,
		Username:  row.Username,
		SetAt:     setAt,
		ExpiresAt: expiresAt,
	}, true, nil
}

func (s *DbStorage) ClearPAT() error {
	if err := s.db.ClearActivePAT(context.Background()); err != nil {
		return fmt.Errorf("could not clear PAT: %w", err)
	}
	return nil
}
