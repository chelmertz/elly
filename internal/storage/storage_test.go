package storage

import (
	"io"
	"testing"
	"time"

	"database/sql"
	"github.com/chelmertz/elly/internal/types"
	"log/slog"
	"path/filepath"
	"strings"
)

func setupTestStorage(t *testing.T) *DbStorage {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store := NewStorage(logger, ":memory:")
	return store
}

func TestStorePAT_InsertsNewPATAsActive(t *testing.T) {
	store := setupTestStorage(t)

	token := "ghp_test123"
	username := "testuser"
	expiresAt := time.Now().Add(30 * 24 * time.Hour)

	err := store.StorePAT(token, username, expiresAt)
	if err != nil {
		t.Fatalf("StorePAT failed: %v", err)
	}

	got, found, err := store.GetPAT()
	if err != nil {
		t.Fatalf("GetPAT failed: %v", err)
	}
	if !found {
		t.Fatal("expected PAT to be found")
	}

	if got.Token != token {
		t.Errorf("expected token %q, got %q", token, got.Token)
	}
	if got.Username != username {
		t.Errorf("expected username %q, got %q", username, got.Username)
	}
}

func TestStorePAT_DeactivatesPreviousPAT(t *testing.T) {
	store := setupTestStorage(t)

	// Store first PAT
	err := store.StorePAT("first_token", "user1", time.Time{})
	if err != nil {
		t.Fatalf("first StorePAT failed: %v", err)
	}

	// Store second PAT
	err = store.StorePAT("second_token", "user2", time.Time{})
	if err != nil {
		t.Fatalf("second StorePAT failed: %v", err)
	}

	// Get PAT should return only the second one
	got, found, err := store.GetPAT()
	if err != nil {
		t.Fatalf("GetPAT failed: %v", err)
	}
	if !found {
		t.Fatal("expected PAT to be found")
	}

	if got.Token != "second_token" {
		t.Errorf("expected token %q, got %q", "second_token", got.Token)
	}
	if got.Username != "user2" {
		t.Errorf("expected username %q, got %q", "user2", got.Username)
	}
}

func TestGetPAT_ReturnsNotFoundWhenNoPAT(t *testing.T) {
	store := setupTestStorage(t)

	_, found, err := store.GetPAT()
	if err != nil {
		t.Fatalf("GetPAT returned unexpected error: %v", err)
	}
	if found {
		t.Fatal("expected found=false when no PAT configured")
	}
}

func TestClearPAT_DeactivatesActivePAT(t *testing.T) {
	store := setupTestStorage(t)

	// Store a PAT
	err := store.StorePAT("test_token", "testuser", time.Time{})
	if err != nil {
		t.Fatalf("StorePAT failed: %v", err)
	}

	// Verify it exists
	_, found, err := store.GetPAT()
	if err != nil {
		t.Fatalf("GetPAT failed before clear: %v", err)
	}
	if !found {
		t.Fatal("expected PAT to be found before clear")
	}

	// Clear the PAT
	err = store.ClearPAT()
	if err != nil {
		t.Fatalf("ClearPAT failed: %v", err)
	}

	// Verify it's gone
	_, found, err = store.GetPAT()
	if err != nil {
		t.Fatalf("GetPAT returned unexpected error after clear: %v", err)
	}
	if found {
		t.Fatal("expected found=false after clearing PAT")
	}
}

func TestStorePAT_HandlesNonExpiringToken(t *testing.T) {
	store := setupTestStorage(t)

	// Store with zero expiration time (non-expiring token)
	err := store.StorePAT("non_expiring_token", "testuser", time.Time{})
	if err != nil {
		t.Fatalf("StorePAT failed: %v", err)
	}

	got, found, err := store.GetPAT()
	if err != nil {
		t.Fatalf("GetPAT failed: %v", err)
	}
	if !found {
		t.Fatal("expected PAT to be found")
	}

	if !got.ExpiresAt.IsZero() {
		t.Errorf("expected zero expiration time for non-expiring token, got %v", got.ExpiresAt)
	}
}

func TestStorePAT_PreservesExpirationTime(t *testing.T) {
	store := setupTestStorage(t)

	expiresAt := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	err := store.StorePAT("expiring_token", "testuser", expiresAt)
	if err != nil {
		t.Fatalf("StorePAT failed: %v", err)
	}

	got, found, err := store.GetPAT()
	if err != nil {
		t.Fatalf("GetPAT failed: %v", err)
	}
	if !found {
		t.Fatal("expected PAT to be found")
	}

	// Compare with second precision (RFC3339 doesn't preserve nanoseconds)
	if !got.ExpiresAt.Truncate(time.Second).Equal(expiresAt.Truncate(time.Second)) {
		t.Errorf("expected expiration %v, got %v", expiresAt, got.ExpiresAt)
	}
}

// A database created before the rereview_from column existed must get the
// column on start and round-trip the field; "create table if not exists"
// alone never adds it.
func TestNewStorage_AddsRereviewColumnToOldDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "elly.db")
	old, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(strings.Replace(ddl, "    rereview_from text not null default '',\n", "", 1)); err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`insert into prs (url, review_status, title, author, repo_name, repo_owner, repo_url, is_draft, last_updated, last_pr_commenter, threads_actionable, threads_waiting, additions, deletions, review_requested_from_users, buried, raw_json_response)
		values ('https://github.com/o/r/pull/1', '', 't', 'me', 'r', 'o', 'https://github.com/o/r', 0, '2026-09-08T10:00:00Z', '', 0, 0, 1, 1, '', 0, '{}')`); err != nil {
		t.Fatal(err)
	}
	_ = old.Close()

	store := NewStorage(slog.New(slog.NewTextHandler(io.Discard, nil)), path)
	prs := store.Prs().Prs
	if len(prs) != 1 || len(prs[0].RereviewFrom) != 0 {
		t.Fatalf("old row must read back with no re-review logins: %+v", prs)
	}
	prs[0].RereviewFrom = []string{"adam", "eve"}
	if err := store.StoreRepoPrs(prs); err != nil {
		t.Fatal(err)
	}
	if got := store.Prs().Prs[0].RereviewFrom; strings.Join(got, ",") != "adam,eve" {
		t.Fatalf("round trip: %v", got)
	}
	// a second start must treat the existing column as a no-op
	NewStorage(slog.New(slog.NewTextHandler(io.Discard, nil)), path)
}

// Burying is per URL and survives a poll, but a PR that was updated since it
// was buried comes back: that is the whole point of burying temporarily.
func TestStoreRepoPrs_BuryPersistsUntilThePrMoves(t *testing.T) {
	store := setupTestStorage(t)
	buried := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	pr := func(url string, updated time.Time) types.ViewPr {
		return types.ViewPr{Url: url, Title: "t", Author: "me", RepoName: "r", RepoOwner: "o",
			LastUpdated: updated, ReviewRequestedFromUsers: []string{}, RereviewFrom: []string{}, RawJsonResponse: []byte("{}")}
	}
	quiet, moved := "https://github.com/o/r/pull/1", "https://github.com/o/r/pull/2"
	if err := store.StoreRepoPrs([]types.ViewPr{pr(quiet, buried), pr(moved, buried)}); err != nil {
		t.Fatal(err)
	}
	for _, url := range []string{quiet, moved} {
		if err := store.Bury(url); err != nil {
			t.Fatal(err)
		}
	}
	// next poll: one PR unchanged, one updated after it was buried
	if err := store.StoreRepoPrs([]types.ViewPr{pr(quiet, buried), pr(moved, buried.Add(time.Hour))}); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, p := range store.Prs().Prs {
		got[p.Url] = p.Buried
	}
	if !got[quiet] {
		t.Error("an unchanged PR must stay buried across polls")
	}
	if got[moved] {
		t.Error("a PR updated since it was buried must be unburied")
	}
	if err := store.Unbury(quiet); err != nil {
		t.Fatal(err)
	}
	for _, p := range store.Prs().Prs {
		if p.Url == quiet && p.Buried {
			t.Error("unbury did not stick")
		}
	}
}

// strings.Split("", ",") is [""], not []. Every list column goes through
// splitLogins for that reason; review_requested_from_users was the one that
// did not, so a PR with nobody requested came back naming one empty reviewer
// and any list built from it said "waiting on review from ".
func TestStoreRepoPrs_EmptyListColumnsComeBackEmpty(t *testing.T) {
	store := setupTestStorage(t)
	pr := types.ViewPr{
		Url: "https://github.com/o/r/pull/1", Title: "t", Author: "me",
		RepoName: "r", RepoOwner: "o", LastUpdated: time.Now(),
		ReviewRequestedFromUsers: []string{}, RereviewFrom: []string{},
		ChecksFailing: []string{}, RawJsonResponse: []byte("{}"),
	}
	if err := store.StoreRepoPrs([]types.ViewPr{pr}); err != nil {
		t.Fatal(err)
	}
	got := store.Prs().Prs[0]
	for name, list := range map[string][]string{
		"ReviewRequestedFromUsers": got.ReviewRequestedFromUsers,
		"RereviewFrom":             got.RereviewFrom,
		"ChecksFailing":            got.ChecksFailing,
	} {
		if len(list) != 0 {
			t.Errorf("%s = %q, want an empty list", name, list)
		}
	}
}

// The mergeability columns arrived after the first release, so an existing
// database has to gain them on startup rather than on a fresh create — the
// same path rereview_from took.
func TestNewStorage_AddsMergeabilityColumnsToOldDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "elly.db")
	oldDdl := strings.Replace(ddl, "    mergeable text not null default '',\n", "", 1)
	oldDdl = strings.Replace(oldDdl, "    merge_state_status text not null default '',\n", "", 1)
	old, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(oldDdl); err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`insert into prs (url, review_status, title, author, repo_name, repo_owner, repo_url, is_draft, last_updated, last_pr_commenter, threads_actionable, threads_waiting, additions, deletions, review_requested_from_users, buried, raw_json_response)
		values ('https://github.com/o/r/pull/1', '', 't', 'me', 'r', 'o', 'https://github.com/o/r', 0, '2026-09-18T10:00:00Z', '', 0, 0, 1, 1, '', 0, '{}')`); err != nil {
		t.Fatal(err)
	}
	_ = old.Close()

	store := NewStorage(slog.New(slog.NewTextHandler(io.Discard, nil)), path)
	prs := store.Prs().Prs
	// An old row knows nothing about its mergeability, and "" must not read as
	// a conflict — that would flag every PR once, immediately after upgrading.
	if len(prs) != 1 || prs[0].Mergeable != "" || prs[0].HasConflict() {
		t.Fatalf("old row must read back with unknown mergeability: %+v", prs)
	}

	prs[0].Mergeable, prs[0].MergeStateStatus = "CONFLICTING", "DIRTY"
	if err := store.StoreRepoPrs(prs); err != nil {
		t.Fatal(err)
	}
	got := store.Prs().Prs[0]
	if got.Mergeable != "CONFLICTING" || got.MergeStateStatus != "DIRTY" || !got.HasConflict() {
		t.Fatalf("round trip: mergeable=%q mergeStateStatus=%q hasConflict=%v", got.Mergeable, got.MergeStateStatus, got.HasConflict())
	}
	// a second start must treat the existing columns as a no-op
	NewStorage(slog.New(slog.NewTextHandler(io.Discard, nil)), path)
}
