package storage

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
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
	oldDdl := strings.Replace(ddl, "    rereview_from text not null default '',\n", "", 1)
	oldDdl = strings.Replace(oldDdl, "    reviewed_by text not null default '',\n", "", 1)
	if _, err := old.Exec(oldDdl); err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`insert into prs (url, review_status, title, author, repo_name, repo_owner, repo_url, is_draft, last_updated, last_pr_commenter, threads_actionable, threads_waiting, additions, deletions, review_requested_from_users, buried, raw_json_response)
		values ('https://github.com/o/r/pull/1', '', 't', 'me', 'r', 'o', 'https://github.com/o/r', 0, '2026-09-08T10:00:00Z', '', 0, 0, 1, 1, '', 0, '{}')`); err != nil {
		t.Fatal(err)
	}
	_ = old.Close()

	store := NewStorage(slog.New(slog.NewTextHandler(io.Discard, nil)), path)
	prs := store.Prs().Prs
	if len(prs) != 1 || len(prs[0].RereviewFrom) != 0 || len(prs[0].ReviewedBy) != 0 {
		t.Fatalf("old row must read back with no re-review logins: %+v", prs)
	}
	prs[0].RereviewFrom = []string{"adam", "eve"}
	prs[0].ReviewedBy = []string{"adam"}
	if err := store.StoreRepoPrs(prs); err != nil {
		t.Fatal(err)
	}
	if got := store.Prs().Prs[0].RereviewFrom; strings.Join(got, ",") != "adam,eve" {
		t.Fatalf("round trip: %v", got)
	}
	if got := store.Prs().Prs[0].ReviewedBy; strings.Join(got, ",") != "adam" {
		t.Fatalf("round trip reviewed_by: %v", got)
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

// The mergeability and head commit columns arrived after the first release, so an existing
// database has to gain them on startup rather than on a fresh create — the
// same path rereview_from took.
func TestNewStorage_AddsMergeabilityColumnsToOldDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "elly.db")
	oldDdl := strings.Replace(ddl, "    mergeable text not null default '',\n", "", 1)
	oldDdl = strings.Replace(oldDdl, "    merge_state_status text not null default '',\n", "", 1)
	oldDdl = strings.Replace(oldDdl, "    head_ref_oid text not null default '',\n", "", 1)
	oldDdl = strings.Replace(oldDdl, "    review_requested_of_user boolean not null default 0,\n", "", 1)
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
	prs[0].HeadRefOid = "0123456789abcdef0123456789abcdef01234567"
	prs[0].ReviewRequestedOfUser = true
	if err := store.StoreRepoPrs(prs); err != nil {
		t.Fatal(err)
	}
	got := store.Prs().Prs[0]
	if got.Mergeable != "CONFLICTING" || got.MergeStateStatus != "DIRTY" || !got.HasConflict() {
		t.Fatalf("round trip: mergeable=%q mergeStateStatus=%q hasConflict=%v", got.Mergeable, got.MergeStateStatus, got.HasConflict())
	}
	if got.HeadRefOid != "0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("round trip: headRefOid=%q", got.HeadRefOid)
	}
	if !got.ReviewRequestedOfUser {
		t.Fatal("round trip: reviewRequestedOfUser lost")
	}
	// a second start must treat the existing columns as a no-op
	NewStorage(slog.New(slog.NewTextHandler(io.Discard, nil)), path)
}

func TestNotifySeen(t *testing.T) {
	store := setupTestStorage(t)

	if got := store.NotifySeen(); got != nil {
		t.Fatalf("fresh database: got %v, want nil so the first fetch is a baseline", got)
	}

	if err := store.StoreNotifySeen(map[string][]string{}); err != nil {
		t.Fatal(err)
	}
	if got := store.NotifySeen(); got == nil {
		t.Error("a stored empty state reads back as nil, so every fetch would be a silent baseline")
	}

	if err := store.StoreNotifySeen(map[string][]string{"u": {"approved"}}); err != nil {
		t.Fatal(err)
	}
	if got := store.NotifySeen(); len(got["u"]) != 1 || got["u"][0] != "approved" {
		t.Errorf("got %v", got)
	}
}

func TestStoreRepoPrsIsAllOrNothing(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store := NewStorage(logger, filepath.Join(t.TempDir(), "elly.db"))
	pr := func(url string) types.ViewPr {
		return types.ViewPr{Url: url, LastUpdated: time.Now(), RawJsonResponse: []byte("{}")}
	}
	if err := store.StoreRepoPrs([]types.ViewPr{pr("old1"), pr("old2")}); err != nil {
		t.Fatal(err)
	}

	broken := pr("new2")
	broken.RawJsonResponse = nil // violates NOT NULL, so the second insert fails
	err := func() (err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("panicked: %v", r)
			}
		}()
		return store.StoreRepoPrs([]types.ViewPr{pr("new1"), broken})
	}()
	if err == nil {
		t.Fatal("a failing insert reported success")
	}

	var urls []string
	for _, p := range store.Prs().Prs {
		urls = append(urls, p.Url)
	}
	if strings.Join(urls, ",") != "old1,old2" && strings.Join(urls, ",") != "old2,old1" {
		t.Errorf("after a failed refresh the stored PRs are %v, want the previous list intact", urls)
	}
}

func TestAnnotations(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store := NewStorage(logger, filepath.Join(t.TempDir(), "elly.db"))
	pr := types.ViewPr{Url: "u", LastUpdated: time.Now(), RawJsonResponse: []byte("{}")}
	if err := store.StoreRepoPrs([]types.ViewPr{pr}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	live := types.Annotation{Source: "tool", State: types.AnnotationNeedsYou, Note: "look",
		Links: []types.AnnotationLink{{Label: "Open", Url: "https://example.com"}}, ExpiresAt: "2099-01-01T01:00:00+01:00"}

	if err := store.PutAnnotation("u", live, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	got := store.Prs().Prs[0].Annotations
	if len(got) != 1 || got[0].Note != "look" || got[0].Links[0].Url != "https://example.com" || got[0].ExpiresAt != live.ExpiresAt {
		t.Fatalf("got %+v, want the annotation as written", got)
	}

	live.State = types.AnnotationHandled
	if err := store.PutAnnotation("u", live, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := store.Prs().Prs[0].Annotations; len(got) != 1 || got[0].State != types.AnnotationHandled {
		t.Errorf("a second write from the same source: got %+v, want it replaced", got)
	}

	if err := store.StoreRepoPrs([]types.ViewPr{pr}); err != nil {
		t.Fatal(err)
	}
	if got := store.Prs().Prs[0].Annotations; len(got) != 1 {
		t.Errorf("a refresh wiped the annotation: %+v", got)
	}

	other := live
	other.Source = "other"
	if err := store.PutAnnotation("u", other, now.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := store.Prs().Prs[0].Annotations; len(got) != 1 || got[0].Source != "tool" {
		t.Errorf("an expired annotation is shown: %+v", got)
	}

	if err := store.DeleteAnnotation("u", "tool"); err != nil {
		t.Fatal(err)
	}
	if got := store.Prs().Prs[0].Annotations; len(got) != 0 {
		t.Errorf("deleted annotation still shown: %+v", got)
	}
}

func TestRateLimit(t *testing.T) {
	store := setupTestStorage(t)
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

	if store.IsRateLimitActive(now) || !store.GetRateLimitUntil().IsZero() {
		t.Fatal("a fresh database is rate limited")
	}
	if err := store.StorePAT("github_pat_main", "me", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if store.IsRateLimitActive(now) {
		t.Fatal("a new token is rate limited")
	}
	until := now.Add(time.Hour)
	if err := store.SetRateLimitUntil(until); err != nil {
		t.Fatal(err)
	}
	if !store.IsRateLimitActive(now) || !store.GetRateLimitUntil().Equal(until) {
		t.Fatalf("limit until %v not in force at %v", store.GetRateLimitUntil(), now)
	}
	if store.IsRateLimitActive(until.Add(time.Second)) {
		t.Error("an expired limit is still in force")
	}
	if !store.GetRateLimitUntil().IsZero() {
		t.Error("an expired limit was not cleared")
	}
	if err := store.SetRateLimitUntil(until); err != nil {
		t.Fatal(err)
	}
	if err := store.StorePAT("github_pat_next", "me", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if store.IsRateLimitActive(now) {
		t.Error("a new token inherited the old one's limit")
	}
}

func TestRateLimitUnreadableCountsAsActive(t *testing.T) {
	store := setupTestStorage(t)
	if err := store.StorePAT("github_pat_main", "me", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.rawDb.Exec(`update token set rate_limited_until = 'garbage' where kind = 'main'`); err != nil {
		t.Fatal(err)
	}
	if !store.IsRateLimitActive(time.Now()) {
		t.Error("an unreadable limit must count as active, the safe side")
	}
}

func TestDegradations(t *testing.T) {
	store := setupTestStorage(t)
	if got := store.Degradations(); got != nil {
		t.Fatalf("fresh database: %v", got)
	}
	ds := []types.Degradation{{Kind: "checks_unreadable", Message: "m", Remedy: "r"}}
	if err := store.StoreDegradations(ds); err != nil {
		t.Fatal(err)
	}
	if got := store.Degradations(); len(got) != 1 || got[0] != ds[0] {
		t.Fatalf("got %v", got)
	}
	if err := store.StoreDegradations(nil); err != nil {
		t.Fatal(err)
	}
	if got := store.Degradations(); len(got) != 0 {
		t.Errorf("a fixed degradation is still reported: %v", got)
	}
}

func TestGetPr(t *testing.T) {
	store := setupTestStorage(t)
	if err := store.StoreRepoPrs([]types.ViewPr{{Url: "u", Title: "t", LastUpdated: time.Now(), RawJsonResponse: []byte("{}")}}); err != nil {
		t.Fatal(err)
	}
	pr, err := store.GetPr("u")
	if err != nil || pr.Title != "t" {
		t.Errorf("got %+v %v", pr, err)
	}
	if _, err := store.GetPr("missing"); err == nil {
		t.Error("a missing PR read without error")
	}
}

// A recording reads the token of a running elly's database, and must neither
// create the file nor touch its schema while doing so.
func TestReadPAT_ReadsWithoutWriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "elly.db")
	store := NewStorage(slog.New(slog.NewTextHandler(io.Discard, nil)), path)
	if err := store.StorePAT("tok", "someone", time.Time{}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	pat, found, err := ReadPAT(path)
	if err != nil || !found || pat.Token != "tok" || pat.Username != "someone" {
		t.Fatalf("got %+v %v %v", pat, found, err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Error("reading the PAT changed the database file")
	}

	missing := filepath.Join(t.TempDir(), "absent.db")
	if _, _, err := ReadPAT(missing); err == nil {
		t.Error("a missing database must be an error")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Error("reading must not create a database")
	}
}

// Each check constraint a released elly created: before running, before
// stuck, and before info. Startup rebuilds either into one that accepts every state.
func TestNewStorage_AcceptsNewStatesOnAnOldAnnotationsTable(t *testing.T) {
	const current = "check (state in ('handled', 'needs_you', 'running', 'stuck', 'info'))"
	for _, oldCheck := range []string{
		"check (state in ('handled', 'needs_you'))",
		"check (state in ('handled', 'needs_you', 'running'))",
		"check (state in ('handled', 'needs_you', 'running', 'stuck'))",
	} {
		t.Run(oldCheck, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "elly.db")
			oldDdl := strings.Replace(ddl, current, oldCheck, 1)
			if oldDdl == ddl {
				t.Fatal("precondition: schema.sql no longer has the check this test rewrites")
			}
			old, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := old.Exec(oldDdl); err != nil {
				t.Fatal(err)
			}
			if _, err := old.Exec(`insert into annotations (pr_url, source, state, note, links, expires_at, expires_unix)
				values ('u', 'other', 'handled', 'kept', '[]', '2099-01-01T00:00:00Z', 4070908800)`); err != nil {
				t.Fatal(err)
			}
			_ = old.Close()

			store := NewStorage(slog.New(slog.NewTextHandler(io.Discard, nil)), path)
			pr := types.ViewPr{Url: "u", LastUpdated: time.Now(), RawJsonResponse: []byte("{}")}
			if err := store.StoreRepoPrs([]types.ViewPr{pr}); err != nil {
				t.Fatal(err)
			}
			for _, state := range []string{types.AnnotationRunning, types.AnnotationStuck, types.AnnotationInfo} {
				a := types.Annotation{Source: "tool-" + state, State: state, ExpiresAt: "2099-01-01T00:00:00Z"}
				if err := store.PutAnnotation("u", a, time.Now().Add(time.Hour)); err != nil {
					t.Fatalf("an upgraded database rejects %s: %v", state, err)
				}
			}
			got := store.Prs().Prs[0].Annotations
			if len(got) != 4 {
				t.Fatalf("got %+v, want the old handled annotation kept beside running, stuck and info", got)
			}
			// a second start must leave the rebuilt table alone
			again := NewStorage(slog.New(slog.NewTextHandler(io.Discard, nil)), path)
			if got := again.Prs().Prs[0].Annotations; len(got) != 4 {
				t.Fatalf("second start: got %+v", got)
			}
		})
	}
}

// seedAccount stores what a poll for one account leaves behind: a PR, buried,
// with an annotation, plus the notification history.
func seedAccount(t *testing.T, store *DbStorage, url string) {
	t.Helper()
	if err := store.StoreRepoPrs([]types.ViewPr{{Url: url, Title: "t", Author: "a", LastUpdated: time.Now(), RawJsonResponse: []byte("{}")}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Bury(url); err != nil {
		t.Fatal(err)
	}
	if err := store.StoreNotifySeen(map[string][]string{url: {"approved"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutAnnotation(url, types.Annotation{Source: "tool", State: types.AnnotationHandled, Links: []types.AnnotationLink{}}, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
}

// Clearing the token leaves nothing that would be served as current: the
// PRs it fetched go, and so does the last poll time, so a token set again
// is polled at once instead of a full interval later.
func TestClearPAT_DeletesTheStoredPrs(t *testing.T) {
	store := setupTestStorage(t)
	if err := store.StorePAT("ghp_a", "me", time.Time{}); err != nil {
		t.Fatal(err)
	}
	seedAccount(t, store, "https://github.com/o/r/pull/1")
	if err := store.ClearPAT(); err != nil {
		t.Fatal(err)
	}
	if got := store.Prs(); len(got.Prs) != 0 || !got.LastFetched.IsZero() {
		t.Errorf("after clearing: %d PRs, last fetched %v", len(got.Prs), got.LastFetched)
	}
}

// Another account's token must not inherit the old account's PRs, buries,
// annotations or notification history: tick and the bar would read them as
// the new login's.
func TestStorePAT_AnotherAccountStartsEmpty(t *testing.T) {
	url := "https://github.com/o/r/pull/1"
	for _, c := range []struct {
		name, second string
		clearFirst   bool
		keeps        bool
	}{
		{"a new token for the same login keeps everything", "me", false, true},
		{"the same login in another case is the same login", "Me", false, true},
		{"another login starts empty", "other", false, false},
		{"another login after a clear starts empty", "other", true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			store := setupTestStorage(t)
			if err := store.StorePAT("ghp_a", "me", time.Time{}); err != nil {
				t.Fatal(err)
			}
			seedAccount(t, store, url)
			if c.clearFirst {
				if err := store.ClearPAT(); err != nil {
					t.Fatal(err)
				}
				// a poll can land between the clear and the next token
				if err := store.StoreRepoPrs([]types.ViewPr{{Url: url, Title: "t", Author: "a", LastUpdated: time.Now(), RawJsonResponse: []byte("{}")}}); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.StorePAT("ghp_b", c.second, time.Time{}); err != nil {
				t.Fatal(err)
			}
			if c.keeps {
				prs := store.Prs().Prs
				if len(prs) != 1 || !prs[0].Buried || len(prs[0].Annotations) != 1 || store.NotifySeen() == nil {
					t.Errorf("same login lost state: %+v, seen %v", prs, store.NotifySeen())
				}
				return
			}
			if got := store.Prs(); len(got.Prs) != 0 || !got.LastFetched.IsZero() {
				t.Errorf("another login inherited %d PRs, last fetched %v", len(got.Prs), got.LastFetched)
			}
			if seen := store.NotifySeen(); seen != nil {
				t.Errorf("another login inherited the notification history %v", seen)
			}
			// the PR comes back for the new login: no bury, no annotation
			if err := store.StoreRepoPrs([]types.ViewPr{{Url: url, Title: "t", Author: "a", LastUpdated: time.Now().Add(-time.Hour), RawJsonResponse: []byte("{}")}}); err != nil {
				t.Fatal(err)
			}
			if prs := store.Prs().Prs; len(prs) != 1 || prs[0].Buried || len(prs[0].Annotations) != 0 {
				t.Errorf("another login inherited the bury or annotation: %+v", prs)
			}
		})
	}
}

// Burying one PR leaves its neighbours alone across polls.
func TestStoreRepoPrs_BuryIsPerPr(t *testing.T) {
	store := setupTestStorage(t)
	at := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	pr := func(url string) types.ViewPr {
		return types.ViewPr{Url: url, Title: "t", Author: "me", RepoName: "r", RepoOwner: "o", LastUpdated: at, RawJsonResponse: []byte("{}")}
	}
	buried, other := "https://github.com/o/r/pull/1", "https://github.com/o/r/pull/2"
	if err := store.StoreRepoPrs([]types.ViewPr{pr(buried), pr(other)}); err != nil {
		t.Fatal(err)
	}
	if err := store.Bury(buried); err != nil {
		t.Fatal(err)
	}
	if err := store.StoreRepoPrs([]types.ViewPr{pr(buried), pr(other)}); err != nil {
		t.Fatal(err)
	}
	for _, p := range store.Prs().Prs {
		if p.Buried != (p.Url == buried) {
			t.Errorf("%s: buried = %v", p.Url, p.Buried)
		}
	}
}

// Prs reports when the PRs were stored, which is what the refresh loop and
// the page's "last refreshed" read.
func TestPrsReportsWhenTheyWereFetched(t *testing.T) {
	store := setupTestStorage(t)
	if got := store.Prs().LastFetched; !got.IsZero() {
		t.Errorf("before any fetch: LastFetched %v", got)
	}
	before := time.Now().Truncate(time.Second)
	if err := store.StoreRepoPrs(nil); err != nil {
		t.Fatal(err)
	}
	if got := store.Prs().LastFetched; got.Before(before) || got.After(time.Now()) {
		t.Errorf("LastFetched %v, want between %v and now", got, before)
	}
}

// A new token is a new quota: the hold the old one ran into goes with it.
func TestStorePATClearsTheRateLimit(t *testing.T) {
	store := setupTestStorage(t)
	if err := store.SetRateLimitUntil(time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := store.StorePAT("ghp_new", "me", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if store.IsRateLimitActive(time.Now()) {
		t.Error("a new token is still held by the old one's rate limit")
	}
}

// A replaced or cleared token is gone from every file sqlite writes, not
// merely marked inactive: an old copy of a token is a live credential until
// it expires.
func TestReplacedAndClearedTokensLeaveNoCopyOnDisk(t *testing.T) {
	dir := t.TempDir()
	store := NewStorage(slog.New(slog.NewTextHandler(io.Discard, nil)), filepath.Join(dir, "elly.db"))
	for _, tok := range []string{"github_pat_first", "github_pat_second"} {
		if err := store.StorePAT(tok, "me", time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.ClearPAT(); err != nil {
		t.Fatal(err)
	}
	assertGone := func(when string) {
		t.Helper()
		if _, err := store.rawDb.Exec(`pragma wal_checkpoint(truncate)`); err != nil {
			t.Fatal(err)
		}
		files, _ := filepath.Glob(filepath.Join(dir, "*"))
		for _, f := range files {
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			for _, old := range []string{"github_pat_first", "github_pat_second"} {
				if bytes.Contains(b, []byte(old)) {
					t.Errorf("%s: %s still holds %s", when, filepath.Base(f), old)
				}
			}
		}
	}
	assertGone("after a clear")
	if err := store.StorePAT("github_pat_third", "me", time.Time{}); err != nil {
		t.Fatal(err)
	}
	assertGone("after a new token")
	got, found, err := store.GetPAT()
	if err != nil || !found || got.Token != "github_pat_third" {
		t.Fatalf("active token %+v %v %v", got, found, err)
	}
}

// oldTokenTables is pat and owner_pat as elly created them before the
// token table, with pat's owner column from 2026-11-05.
const oldTokenTables = `
create table pat (pat text primary key, set_at text not null default (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
	expires_at text not null, username text not null, active integer not null check (active in (0, 1)),
	owner text not null default '');
create table owner_pat (owner text not null primary key collate nocase, pat text not null, username text not null,
	set_at text not null default (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')), expires_at text not null,
	last_success text not null default '', rate_limited_until text not null default '', refused text not null default '');
pragma user_version = 0;`

// oldDatabase is a database in the shape elly kept before the token table:
// NewStorage's tables, with pat and owner_pat put back and stmts run.
func oldDatabase(t *testing.T, stmts ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "elly.db")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := NewStorage(logger, path)
	if err := s.StoreRepoPrs([]types.ViewPr{{Url: "https://github.com/o/r/pull/1", LastUpdated: time.Now(), RawJsonResponse: []byte("{}")}}); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range append([]string{`drop table token`, oldTokenTables}, stmts...) {
		if _, err := s.rawDb.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	s.rawDb.Close() //nolint:errcheck
	return path
}

// A database from before tokens were deleted keeps deactivated ones;
// opening it moves only the active one and keeps the newest login, so a
// token for another login still starts empty.
func TestOpeningAnOldDatabaseDropsDeactivatedTokens(t *testing.T) {
	path := oldDatabase(t,
		`insert into pat (pat, expires_at, username, active) values ('github_pat_old', '', 'someone', 0)`,
		`insert into pat (pat, expires_at, username, active) values ('github_pat_me', '', 'me', 1)`,
		`delete from meta where key = 'last_login'`,
	)
	store := NewStorage(slog.New(slog.NewTextHandler(io.Discard, nil)), path)
	var left string
	if err := store.rawDb.QueryRow(`select group_concat(pat) from token`).Scan(&left); err != nil || left != "github_pat_me" {
		t.Fatalf("tokens left %q, %v", left, err)
	}
	if err := store.StorePAT("github_pat_other", "other", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if got := store.Prs().Prs; len(got) != 0 {
		t.Errorf("another login inherited %v", got)
	}
}

// Opening a database from before the token table moves both kinds of token,
// with the main token's rate limit and every org token's poll state, into
// token; drops an org token of another login; keeps last_fetched in meta;
// and leaves nothing of the old tables. A second open changes nothing.
func TestOpeningAnOldDatabaseMovesTokens(t *testing.T) {
	path := oldDatabase(t,
		`insert into pat (pat, set_at, expires_at, username, active, owner) values ('github_pat_main', '2026-11-16T09:38:27Z', '2027-11-17T09:35:55Z', 'me', 1, 'acme')`,
		`replace into meta (key, value) values ('rate_limit_until', '2026-11-18T13:00:00Z'), ('last_fetched', '2026-11-18T12:00:00Z')`,
		`insert into owner_pat (owner, pat, username, set_at, expires_at, last_success, rate_limited_until, refused) values
			('Org', 'github_pat_org', 'Me', '2026-11-16T12:44:31Z', '2027-11-06T23:53:34Z', '2026-11-18T12:00:00Z', '2026-11-18T14:00:00Z', 'owner_token_refused'),
			('stray', 'github_pat_stray', 'someone', '2026-11-01T00:00:00Z', '', '', '', '')`,
	)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for open := range 2 {
		store := NewStorage(logger, path)
		main, found, err := store.GetPAT()
		if err != nil || !found || main.Token != "github_pat_main" || main.Username != "me" || main.Owner != "acme" ||
			main.SetAt.Format(time.RFC3339) != "2026-11-16T09:38:27Z" || main.ExpiresAt.Format(time.RFC3339) != "2027-11-17T09:35:55Z" {
			t.Fatalf("open %d: main %+v %v %v", open, main, found, err)
		}
		if got := store.GetRateLimitUntil().Format(time.RFC3339); got != "2026-11-18T13:00:00Z" {
			t.Errorf("open %d: main rate limit %s", open, got)
		}
		orgs, err := store.OwnerPATs()
		if err != nil || len(orgs) != 1 {
			t.Fatalf("open %d: orgs %+v %v", open, orgs, err)
		}
		o := orgs[0]
		if o.Owner != "Org" || o.Token != "github_pat_org" || o.Refused != "owner_token_refused" ||
			o.LastSuccess.Format(time.RFC3339) != "2026-11-18T12:00:00Z" || o.RateLimitedUntil.Format(time.RFC3339) != "2026-11-18T14:00:00Z" ||
			o.ExpiresAt.Format(time.RFC3339) != "2027-11-06T23:53:34Z" {
			t.Errorf("open %d: org %+v", open, o)
		}
		var fetched string
		var old int
		if err := store.rawDb.QueryRow(`select value from meta where key = 'last_fetched'`).Scan(&fetched); err != nil || fetched != "2026-11-18T12:00:00Z" {
			t.Errorf("open %d: last_fetched %q %v", open, fetched, err)
		}
		if err := store.rawDb.QueryRow(`select count(*) from sqlite_master where name in ('pat', 'owner_pat')
			union all select count(*) from meta where key = 'rate_limit_until'`).Scan(&old); err != nil || old != 0 {
			t.Errorf("open %d: old tables or keys left: %d %v", open, old, err)
		}
		store.rawDb.Close() //nolint:errcheck
	}
}

// The main token and org tokens share the table; nothing aimed at one kind
// reaches the other, and an org row can never take the main row's place.
func TestTokenKindsStayApart(t *testing.T) {
	store := setupTestStorage(t)
	if err := store.StorePAT("github_pat_main", "me", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutOwnerPAT("acme", "github_pat_main", "me", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if orgs, _ := store.OwnerPATs(); len(orgs) != 1 || orgs[0].Owner != "acme" {
		t.Fatalf("OwnerPATs listed the main token: %+v", orgs)
	}
	// The same token in both kinds: an expiry or owner found for the main
	// token stays on the main row.
	exp := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := store.SetPATExpiry("github_pat_main", exp); err != nil {
		t.Fatal(err)
	}
	if err := store.SetPATOwner("github_pat_main", "me"); err != nil {
		t.Fatal(err)
	}
	if orgs, _ := store.OwnerPATs(); !orgs[0].ExpiresAt.IsZero() || orgs[0].Owner != "acme" {
		t.Errorf("a main-token write reached the org row: %+v", orgs[0])
	}
	if err := store.SetRateLimitUntil(exp); err != nil {
		t.Fatal(err)
	}
	if orgs, _ := store.OwnerPATs(); !orgs[0].RateLimitedUntil.IsZero() {
		t.Errorf("the main rate limit reached the org row: %+v", orgs[0])
	}
	if err := store.PutOwnerPAT("", "github_pat_x", "me", time.Time{}); err == nil {
		t.Error("an org token with no owner was stored")
	}
	if gone, err := store.DeleteOwnerPAT(""); err != nil || gone {
		t.Errorf("deleting org '' removed something: %v %v", gone, err)
	}
	if _, found, _ := store.GetPAT(); !found {
		t.Fatal("an org-side call removed the main token")
	}
	if err := store.StorePAT("github_pat_rotated", "me", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if orgs, _ := store.OwnerPATs(); len(orgs) != 1 {
		t.Errorf("rotating the main token removed org tokens: %+v", orgs)
	}
	if err := store.ClearPAT(); err != nil {
		t.Fatal(err)
	}
	if orgs, _ := store.OwnerPATs(); len(orgs) != 1 {
		t.Errorf("ClearPAT removed org tokens: %+v", orgs)
	}
	if _, err := store.rawDb.Exec(`insert into token (kind, pat, username, expires_at) values ('main', 'a', 'me', ''), ('main', 'b', 'me', '')`); err == nil {
		t.Error("two main tokens were stored")
	}
}

func TestOwnerPATNeedsThePrimarysLogin(t *testing.T) {
	store := setupTestStorage(t)
	if err := store.PutOwnerPAT("acme", "github_pat_acme", "me", time.Time{}); !errors.Is(err, ErrNoPrimary) {
		t.Fatalf("without a primary: %v", err)
	}
	if err := store.StorePAT("github_pat_main", "Me", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutOwnerPAT("acme", "github_pat_acme", "someone", time.Time{}); !errors.Is(err, ErrOtherLogin) {
		t.Fatalf("another login: %v", err)
	}
	if err := store.PutOwnerPAT("acme", "github_pat_acme", "me", time.Time{}); err != nil {
		t.Fatalf("logins are case-insensitive: %v", err)
	}
	got, err := store.OwnerPATs()
	if err != nil || len(got) != 1 || got[0].Token != "github_pat_acme" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestOwnerPATReplacesInPlaceAndResetsItsRefusal(t *testing.T) {
	store := setupTestStorage(t)
	if err := store.StorePAT("github_pat_main", "me", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutOwnerPAT("acme", "github_pat_one", "me", time.Time{}); err != nil {
		t.Fatal(err)
	}
	success := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	if err := store.SetOwnerPollState(OwnerPAT{Owner: "acme", Token: "github_pat_one", LastSuccess: success, Refused: "owner_token_refused"}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutOwnerPAT("Acme", "github_pat_two", "me", time.Time{}); err != nil {
		t.Fatal(err)
	}
	// A poll that started with the old token reports late; it must not
	// mark the new one refused.
	if err := store.SetOwnerPollState(OwnerPAT{Owner: "acme", Token: "github_pat_one", Refused: "owner_token_refused"}); err != nil {
		t.Fatal(err)
	}
	got, _ := store.OwnerPATs()
	if len(got) != 1 {
		t.Fatalf("%+v", got)
	}
	o := got[0]
	if o.Owner != "Acme" || o.Token != "github_pat_two" || o.Refused != "" || !o.LastSuccess.Equal(success) {
		t.Errorf("%+v", o)
	}
}

func TestOwnerPATsFollowThePrimarysLogin(t *testing.T) {
	store := setupTestStorage(t)
	if err := store.StorePAT("github_pat_main", "me", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutOwnerPAT("acme", "github_pat_acme", "me", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := store.ClearPAT(); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.OwnerPATs(); len(got) != 1 {
		t.Fatalf("a clear dropped the owner tokens: %+v", got)
	}
	if err := store.StorePAT("github_pat_main2", "ME", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.OwnerPATs(); len(got) != 1 {
		t.Fatalf("the same login dropped the owner tokens: %+v", got)
	}
	if err := store.StorePAT("github_pat_other", "other", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.OwnerPATs(); len(got) != 0 {
		t.Fatalf("another login kept %+v", got)
	}
	if ok, err := store.DeleteOwnerPAT("acme"); ok || err != nil {
		t.Errorf("deleted a token that was gone: %v %v", ok, err)
	}
}

func TestDeleteOwnerPATSaysItDeletedOne(t *testing.T) {
	store := setupTestStorage(t)
	if err := store.StorePAT("github_pat_main", "me", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutOwnerPAT("acme", "github_pat_acme", "me", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if ok, err := store.DeleteOwnerPAT("ACME"); !ok || err != nil {
		t.Fatalf("%v %v", ok, err)
	}
	if got, _ := store.OwnerPATs(); len(got) != 0 {
		t.Errorf("left %+v", got)
	}
}

// An expiry lands only on the token it was read for: a token replaced in the
// meantime keeps its own.
func TestSetExpiryOnlyTouchesTheSameToken(t *testing.T) {
	s := NewStorage(slog.New(slog.NewTextHandler(io.Discard, nil)), filepath.Join(t.TempDir(), "elly.db"))
	if err := s.StorePAT("github_pat_new", "me", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutOwnerPAT("acme", "github_pat_acmenew", "me", time.Time{}); err != nil {
		t.Fatal(err)
	}
	when := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, err := range []error{s.SetPATExpiry("github_pat_old", when), s.SetOwnerPATExpiry("acme", "github_pat_acmeold", when), s.SetPATOwner("github_pat_old", "Acme")} {
		if err != nil {
			t.Fatal(err)
		}
	}
	pat, _, _ := s.GetPAT()
	owners, _ := s.OwnerPATs()
	if !pat.ExpiresAt.IsZero() || !owners[0].ExpiresAt.IsZero() || pat.Owner != "" {
		t.Fatalf("a replaced token's expiry or owner landed on its successor: %v, %v, %q", pat.ExpiresAt, owners[0].ExpiresAt, pat.Owner)
	}
	if err := s.SetPATOwner("github_pat_new", "Acme"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPATExpiry("github_pat_new", when); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOwnerPATExpiry("acme", "github_pat_acmenew", when); err != nil {
		t.Fatal(err)
	}
	pat, _, _ = s.GetPAT()
	owners, _ = s.OwnerPATs()
	if !pat.ExpiresAt.Equal(when) || !owners[0].ExpiresAt.Equal(when) || pat.Owner != "Acme" {
		t.Errorf("expiry or owner not stored: %v, %v, %q", pat.ExpiresAt, owners[0].ExpiresAt, pat.Owner)
	}
}

// Every connection runs in WAL with a busy timeout: without them a write
// that met another connection failed at once with SQLITE_BUSY.
func TestStorageWaitsOutALockedDatabase(t *testing.T) {
	s := NewStorage(slog.New(slog.NewTextHandler(io.Discard, nil)), filepath.Join(t.TempDir(), "elly.db"))
	var mode string
	var timeout int
	if err := s.rawDb.QueryRow("pragma journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Errorf("journal_mode %q, %v", mode, err)
	}
	if err := s.rawDb.QueryRow("pragma busy_timeout").Scan(&timeout); err != nil || timeout != 5000 {
		t.Errorf("busy_timeout %d, %v", timeout, err)
	}
}

// A poll whose request search could not say leaves Asked "" and storage
// keeps the last poll's answer, so a team request does not flap to "not
// asked" on one failed search; a PR it never saw asked reads not asked.
// Rows from before the column read their old bool.
func TestStoreRepoPrs_UnknownAskedKeepsTheLastPollsAnswer(t *testing.T) {
	store := setupTestStorage(t)
	pr := func(url, asked string) types.ViewPr {
		return types.ViewPr{Url: url, Title: "t", Author: "adam", RepoName: "r", RepoOwner: "o", Asked: asked,
			ReviewRequestedFromUsers: []string{}, RereviewFrom: []string{}, RawJsonResponse: []byte("{}")}
	}
	asked, fresh := "https://github.com/o/r/pull/1", "https://github.com/o/r/pull/2"
	if err := store.StoreRepoPrs([]types.ViewPr{pr(asked, types.AskedRequested)}); err != nil {
		t.Fatal(err)
	}
	if err := store.StoreRepoPrs([]types.ViewPr{pr(asked, ""), pr(fresh, "")}); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, p := range store.Prs().Prs {
		got[p.Url] = p.Asked
	}
	if got[asked] != types.AskedRequested || got[fresh] != types.AskedNotAsked {
		t.Errorf("after an unreadable search: %v", got)
	}
	if err := store.StoreRepoPrs([]types.ViewPr{pr(asked, types.AskedNotAsked)}); err != nil {
		t.Fatal(err)
	}
	if p := store.Prs().Prs[0]; p.Asked != types.AskedNotAsked {
		t.Errorf("a withdrawn request stuck: %q", p.Asked)
	}
}

func TestNewStorage_SeedsAskedFromTheOldColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "elly.db")
	old, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(strings.Replace(ddl, "    asked text not null default '',\n", "", 1)); err != nil {
		t.Fatal(err)
	}
	for n, requested := range map[string]int{"1": 1, "2": 0} {
		if _, err := old.Exec(`insert into prs (url, review_status, title, author, repo_name, repo_owner, repo_url, is_draft, last_updated, last_pr_commenter, threads_actionable, threads_waiting, additions, deletions, review_requested_from_users, buried, review_requested_of_user, raw_json_response)
			values ('https://github.com/o/r/pull/`+n+`', '', 't', 'adam', 'r', 'o', 'https://github.com/o/r', 0, '2026-09-08T10:00:00Z', '', 0, 0, 1, 1, '', 0, ?, '{}')`, requested); err != nil {
			t.Fatal(err)
		}
	}
	_ = old.Close()
	got := map[string]string{}
	for _, p := range NewStorage(slog.New(slog.NewTextHandler(io.Discard, nil)), path).Prs().Prs {
		got[p.Url[len(p.Url)-1:]] = p.Asked
	}
	if got["1"] != types.AskedRequested || got["2"] != types.AskedNotAsked {
		t.Errorf("seeded %v", got)
	}
}

// Mentions round-trip, and a stored list elly cannot read reports the PR
// unreadable rather than free of mentions.
func TestStoreRepoPrs_MentionsRoundTrip(t *testing.T) {
	store := setupTestStorage(t)
	at := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
	m := types.Mention{Url: "https://github.com/o/r/pull/1#issuecomment-9", Author: "adam", At: at, BodyHash: "ab"}
	pr := types.ViewPr{
		Url: "https://github.com/o/r/pull/1", Title: "t", Author: "me",
		RepoName: "r", RepoOwner: "o", LastUpdated: time.Now(),
		RawJsonResponse: []byte("{}"), Mentions: []types.Mention{m}, MentionsReadable: true,
	}
	if err := store.StoreRepoPrs([]types.ViewPr{pr}); err != nil {
		t.Fatal(err)
	}
	got := store.Prs().Prs[0]
	if len(got.Mentions) != 1 || got.Mentions[0] != m || !got.MentionsReadable {
		t.Fatalf("got %+v readable %v", got.Mentions, got.MentionsReadable)
	}
	if _, err := store.rawDb.Exec(`update prs set mentions = 'garbage'`); err != nil {
		t.Fatal(err)
	}
	if got := store.Prs().Prs[0]; got.MentionsReadable || len(got.Mentions) != 0 {
		t.Errorf("an unreadable list: %+v readable %v", got.Mentions, got.MentionsReadable)
	}
}

// A pat table from before its owner column (2026-11-05) still migrates; the
// main token gets no owner.
func TestOpeningAnOldDatabaseWithoutTheOwnerColumn(t *testing.T) {
	path := oldDatabase(t,
		`alter table pat drop column owner`,
		`insert into pat (pat, expires_at, username, active) values ('github_pat_me', '', 'me', 1)`,
	)
	store := NewStorage(slog.New(slog.NewTextHandler(io.Discard, nil)), path)
	var pat, owner string
	if err := store.rawDb.QueryRow(`select pat, owner from token where kind = 'main'`).Scan(&pat, &owner); err != nil || pat != "github_pat_me" || owner != "" {
		t.Fatalf("main token %q owner %q, %v", pat, owner, err)
	}
}
