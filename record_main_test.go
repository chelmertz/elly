package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chelmertz/elly/internal/storage"
)

var authSeen string

func recordFixture(t *testing.T) (dbPath string, srv *httptest.Server, requests *int) {
	t.Helper()
	dbPath = filepath.Join(t.TempDir(), "elly.db")
	store := storage.NewStorage(slog.New(slog.NewTextHandler(io.Discard, nil)), dbPath)
	if err := store.StorePAT("tok", "someone", time.Time{}); err != nil {
		t.Fatal(err)
	}
	n := 0
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		authSeen = r.Header.Get("Authorization")
		_, _ = w.Write(graphqlSearchResponse())
	}))
	t.Cleanup(srv.Close)
	return dbPath, srv, &n
}

// -record is one poll and exit, written to the next free file so a second
// recording never overwrites the first.
func TestRecord_WritesTheNextFreeFile(t *testing.T) {
	dbPath, srv, requests := recordFixture(t)
	dir := filepath.Join(t.TempDir(), "recordings")
	for _, want := range []string{"poll-1.json", "poll-2.json"} {
		path, err := record(dir, []string{"o"}, false, dbPath, srv.URL, testLogger(t))
		if err != nil {
			t.Fatal(err)
		}
		if filepath.Base(path) != want {
			t.Errorf("wrote %s, want %s", path, want)
		}
		body, err := os.ReadFile(path)
		if err != nil || !strings.Contains(string(body), `"pollTime"`) {
			t.Errorf("not a recording: %v", err)
		}
	}
	// two searches per poll, and no token validation round trip
	if *requests != 4 {
		t.Errorf("expected 4 requests for two polls, got %d", *requests)
	}
}

func TestRecord_RefusesBeforeAnyRequest(t *testing.T) {
	dbPath, srv, requests := recordFixture(t)
	dir := t.TempDir()
	if _, err := record(dir, []string{"o"}, true, dbPath, srv.URL, testLogger(t)); err == nil {
		t.Error("-verbose logs bodies, so -record must refuse it")
	}
	if _, err := record(dir, nil, false, dbPath, srv.URL, testLogger(t)); err == nil {
		t.Error("-record without an owner allowlist would record every org the token reaches")
	}
	if _, err := record(dir, []string{"o"}, false, filepath.Join(dir, "absent.db"), srv.URL, testLogger(t)); err == nil {
		t.Error("no database, no token")
	}
	if *requests != 0 {
		t.Errorf("a refused recording asked github %d times", *requests)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("a refused recording left files: %v", entries)
	}
}

// An owner with its own token is recorded with it, as the live poll reads
// that owner; the main token may not reach its PRs.
func TestRecord_UsesTheOwnerToken(t *testing.T) {
	dbPath, srv, _ := recordFixture(t)
	store := storage.NewStorage(slog.New(slog.NewTextHandler(io.Discard, nil)), dbPath)
	if err := store.PutOwnerPAT("o", "ownertok", "someone", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, err := record(t.TempDir(), []string{"o"}, false, dbPath, srv.URL, testLogger(t)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(authSeen, "ownertok") {
		t.Errorf("recorded with %q, want the owner token", authSeen)
	}
	if _, err := record(t.TempDir(), []string{"other"}, false, dbPath, srv.URL, testLogger(t)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(authSeen, "tok") || strings.Contains(authSeen, "ownertok") {
		t.Errorf("an owner without a token recorded with %q, want the main token", authSeen)
	}
}
