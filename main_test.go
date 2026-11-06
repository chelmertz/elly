package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chelmertz/elly/internal/backoff"
	"github.com/chelmertz/elly/internal/github"
	"github.com/chelmertz/elly/internal/notify"
	"github.com/chelmertz/elly/internal/storage"
	"github.com/chelmertz/elly/internal/types"
)

func testLogger(t *testing.T) *slog.Logger {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{AddSource: true}))
	t.Cleanup(func() {
		if t.Failed() {
			t.Log(buf.String())
		}
	})
	return logger
}

// testStorage is a minimal in-memory storage for testing initPAT.
type testStorage struct {
	degradations []types.Degradation
	pat          storage.StoredPAT
	hasPAT       bool
	cleared      bool
}

func newTestStorage(token, username string) *testStorage {
	return &testStorage{
		pat: storage.StoredPAT{
			Token:    token,
			Username: username,
		},
		hasPAT: true,
	}
}

func (s *testStorage) GetPAT() (storage.StoredPAT, bool, error) {
	if !s.hasPAT || s.cleared {
		return storage.StoredPAT{}, false, nil
	}
	return s.pat, true, nil
}

func (s *testStorage) StorePAT(token, username string, expiresAt time.Time) error {
	s.pat = storage.StoredPAT{Token: token, Username: username, ExpiresAt: expiresAt}
	s.hasPAT = true
	s.cleared = false
	return nil
}

func (s *testStorage) ClearPAT() error {
	s.cleared = true
	return nil
}

func (s *testStorage) Prs() storage.StoredState                  { return storage.StoredState{} }
func (s *testStorage) StoreRepoPrs([]types.ViewPr) error         { return nil }
func (s *testStorage) Bury(string) error                         { return nil }
func (s *testStorage) Unbury(string) error                       { return nil }
func (s *testStorage) GetPr(string) (storage.Pr, error)          { return storage.Pr{}, nil }
func (s *testStorage) SetRateLimitUntil(time.Time) error         { return nil }
func (s *testStorage) IsRateLimitActive(time.Time) bool          { return false }
func (s *testStorage) GetRateLimitUntil() time.Time              { return time.Time{} }
func (s *testStorage) StoreNotifySeen(map[string][]string) error { return nil }
func (s *testStorage) NotifySeen() map[string][]string           { return nil }
func (s *testStorage) PutAnnotation(string, types.Annotation, time.Time) error {
	return nil
}
func (s *testStorage) DeleteAnnotation(string, string) error { return nil }
func (s *testStorage) PutOwnerPAT(string, string, string, time.Time) error {
	return nil
}
func (s *testStorage) OwnerPATs() ([]storage.OwnerPAT, error)     { return nil, nil }
func (s *testStorage) DeleteOwnerPAT(string) (bool, error)        { return false, nil }
func (s *testStorage) SetOwnerPollState(o storage.OwnerPAT) error { return nil }
func (s *testStorage) SetPATExpiry(string, time.Time) error        { return nil }
func (s *testStorage) SetOwnerPATExpiry(string, string, time.Time) error {
	return nil
}

var _ storage.Storage = (*testStorage)(nil)

// graphqlViewerResponse returns a valid GraphQL viewer response body.
func graphqlViewerResponse(username string) []byte {
	resp := struct {
		Data struct {
			Viewer struct {
				Login string `json:"login"`
			} `json:"viewer"`
		} `json:"data"`
	}{}
	resp.Data.Viewer.Login = username
	b, err := json.Marshal(resp)
	if err != nil {
		panic(err)
	}
	return b
}

// graphqlSearchResponse returns a valid empty PR search response body.
func graphqlSearchResponse() []byte {
	resp := struct {
		Data struct {
			Search struct {
				Edges []struct{} `json:"edges"`
			} `json:"search"`
		} `json:"data"`
	}{}
	b, err := json.Marshal(resp)
	if err != nil {
		panic(err)
	}
	return b
}

func graphqlRateLimitedResponse() []byte {
	resp := struct {
		Errors []struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"errors"`
	}{
		Errors: []struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		}{
			{Type: "RATE_LIMITED", Message: "API rate limit exceeded"},
		},
	}
	b, err := json.Marshal(resp)
	if err != nil {
		panic(err)
	}
	return b
}

func TestInitPAT_StoredPATValidation(t *testing.T) {
	viewerThenSearchHandler := func() http.Handler {
		callCount := 0
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			callCount++
			if callCount == 1 {
				w.Write(graphqlViewerResponse("testuser"))
				return
			}
			w.Write(graphqlSearchResponse())
		})
	}

	viewerThenRateLimitHandler := func() http.Handler {
		callCount := 0
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			callCount++
			if callCount == 1 {
				w.Write(graphqlViewerResponse("testuser"))
				return
			}
			w.Header().Set("x-ratelimit-reset", "9999999999")
			w.Write(graphqlRateLimitedResponse())
		})
	}

	viewerThenStatusHandler := func(status int, body string) http.Handler {
		callCount := 0
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			callCount++
			if callCount == 1 {
				w.Write(graphqlViewerResponse("testuser"))
				return
			}
			w.WriteHeader(status)
			w.Write([]byte(body))
		})
	}

	fixedStatusHandler := func(status int, body string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			w.Write([]byte(body))
		})
	}

	tests := []struct {
		name          string
		handler       http.Handler
		wantSetupMode bool
	}{
		{
			name:          "valid token",
			handler:       viewerThenSearchHandler(),
			wantSetupMode: false,
		},
		{
			name:          "rate limited",
			handler:       viewerThenRateLimitHandler(),
			wantSetupMode: false,
		},
		{
			name:          "server error",
			handler:       fixedStatusHandler(500, `{"errors":[]}`),
			wantSetupMode: false,
		},
		{
			name:          "client error on the PR query keeps the token",
			handler:       viewerThenStatusHandler(404, `{"message":"Not Found"}`),
			wantSetupMode: false,
		},
		{
			name:          "secondary rate limit on the PR query keeps the token",
			handler:       viewerThenStatusHandler(403, `{"message":"You have exceeded a secondary rate limit"}`),
			wantSetupMode: false,
		},
		{
			name:          "429 on the PR query keeps the token",
			handler:       viewerThenStatusHandler(429, `{"message":"slow down"}`),
			wantSetupMode: false,
		},
		{
			name:          "an html page on the PR query keeps the token",
			handler:       viewerThenStatusHandler(200, `<html>GitHub is down for maintenance</html>`),
			wantSetupMode: false,
		},
		{
			name:          "an html page on the login query keeps the token",
			handler:       fixedStatusHandler(200, `<html>GitHub is down for maintenance</html>`),
			wantSetupMode: false,
		},
		{
			name:          "429 on the login query keeps the token",
			handler:       fixedStatusHandler(429, `{"message":"slow down"}`),
			wantSetupMode: false,
		},
		{
			name:          "server error with html body on the PR query is transient",
			handler:       viewerThenStatusHandler(502, `<html>Bad gateway</html>`),
			wantSetupMode: false,
		},
		{
			name:          "401 unauthorized",
			handler:       fixedStatusHandler(401, `{"message":"Bad credentials"}`),
			wantSetupMode: true,
		},
		{
			name:          "403 is a secondary rate limit as often as a refusal, so it keeps the token",
			handler:       fixedStatusHandler(403, `{"message":"You have exceeded a secondary rate limit"}`),
			wantSetupMode: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			githubAPI := httptest.NewServer(tt.handler)
			defer githubAPI.Close()

			store := newTestStorage("test-token", "testuser")
			setupMode, err := initPAT(store, githubAPI.URL, testLogger(t))

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if setupMode != tt.wantSetupMode {
				t.Errorf("setupMode = %v, want %v", setupMode, tt.wantSetupMode)
			}
			if store.cleared != tt.wantSetupMode {
				t.Errorf("cleared = %v, want %v", store.cleared, tt.wantSetupMode)
			}
		})
	}
}

func TestInitPAT_NetworkError_KeepsPAT(t *testing.T) {
	store := newTestStorage("test-token", "testuser")
	setupMode, err := initPAT(store, "http://127.0.0.1:1", testLogger(t))

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if setupMode {
		t.Error("setupMode = true, want false")
	}
	if store.cleared {
		t.Error("cleared = true, want false")
	}
}

func TestErrInvalidToken_WrappedCorrectly(t *testing.T) {
	wrapped := errors.Join(github.ErrInvalidToken, errors.New("some detail"))
	if !errors.Is(wrapped, github.ErrInvalidToken) {
		t.Error("expected errors.Is to find ErrInvalidToken")
	}
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestRefreshLoop_ClientErrorBacksOffAndKeepsPolling(t *testing.T) {
	var calls atomic.Int32
	githubAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(404)
		w.Write([]byte(`{"message":"Not Found"}`))
	}))
	defer githubAPI.Close()

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	tracker := backoff.New(logger, time.Hour)
	defer tracker.Stop()

	gaveUp := make(chan struct{})
	go func() {
		startRefreshLoop(newTestStorage("test-token", "testuser"), tracker, githubAPI.URL, nil, logger)
		close(gaveUp)
	}()

	waitFor(t, "first fetch", func() bool { return calls.Load() == 1 })
	tracker.RequestRefresh()
	waitFor(t, "second fetch after a client error", func() bool { return calls.Load() == 2 })

	select {
	case <-gaveUp:
		t.Fatal("refresh loop stopped after a client error, expected it to keep polling")
	default:
	}
	if !strings.Contains(logs.String(), "backing off") {
		t.Fatalf("expected a backoff log line after a client error, got:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), "could not fetch prs from github") {
		t.Fatalf("expected the failed fetch to be logged with its error, got:\n%s", logs.String())
	}
}

func (s *testStorage) StoreDegradations(ds []types.Degradation) error {
	s.degradations = ds
	return nil
}

func (s *testStorage) Degradations() []types.Degradation { return s.degradations }

func TestNotifyChanges_BaselineThenOnePerGainedReason(t *testing.T) {
	logger := testLogger(t)
	store := storage.NewStorage(logger, ":memory:")
	var sent []string
	send := func(n notify.Notification) { sent = append(sent, n.PrUrl) }
	pr := types.ViewPr{Url: "https://github.com/work/r/pull/1", RepoOwner: "work", Author: "me", RawJsonResponse: []byte("{}"),
		LastUpdated: time.Now().UTC().Truncate(time.Second)}

	pr.ReviewStatus = "APPROVED"
	if err := store.StoreRepoPrs([]types.ViewPr{pr}); err != nil {
		t.Fatal(err)
	}
	notifyChanges(store, "me", []string{"work"}, send, logger)
	if len(sent) != 0 {
		t.Fatalf("first fetch sent %v, want a silent baseline", sent)
	}

	pr.ReviewStatus = "CHANGES_REQUESTED"
	if err := store.StoreRepoPrs([]types.ViewPr{pr}); err != nil {
		t.Fatal(err)
	}
	notifyChanges(store, "me", []string{"work"}, send, logger)
	notifyChanges(store, "me", []string{"work"}, send, logger)
	if len(sent) != 1 {
		t.Errorf("sent %v, want exactly one notification for the change request", sent)
	}
}

func TestSplitOwners(t *testing.T) {
	got := strings.Join(splitOwners(" acme, ,globex,"), "|")
	if got != "acme|globex" {
		t.Errorf("got %q", got)
	}
}

func TestAnnotationTokenPath(t *testing.T) {
	if got := annotationTokenPath("", "/run/user/1"); got != "/run/user/1/elly/annotation-token" {
		t.Errorf("default: %q", got)
	}
	if got := annotationTokenPath("/x/token", "/run/user/1"); got != "/x/token" {
		t.Errorf("flag: %q", got)
	}
	if got := annotationTokenPath("", ""); got != "" {
		t.Errorf("no runtime dir: %q, want writes disabled", got)
	}
}

// A fine-grained token makes every poll report "checks_unreadable", and
// nothing short of a different token type changes that. One INFO line per
// process says so; a WARN every poll was noise. Other degradations still warn
// on every poll they occur in.
func TestLogDegradations_TokenLimitationOnceAtInfo(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	announced := map[string]bool{}
	poll := []types.Degradation{
		{Kind: "checks_unreadable", Message: "checks-message"},
		{Kind: "review_requests_unreadable", Message: "requests-message"},
	}
	for range 3 {
		logDegradations(poll, announced, logger)
	}

	var checksInfo, checksWarn, requestsWarn int
	for _, line := range strings.Split(logs.String(), "\n") {
		switch {
		case strings.Contains(line, "checks_unreadable") && strings.Contains(line, "level=INFO"):
			checksInfo++
		case strings.Contains(line, "checks_unreadable") && strings.Contains(line, "level=WARN"):
			checksWarn++
		case strings.Contains(line, "review_requests_unreadable") && strings.Contains(line, "level=WARN"):
			requestsWarn++
		}
	}
	if checksInfo != 1 || checksWarn != 0 {
		t.Errorf("checks_unreadable: want 1 INFO and 0 WARN over 3 polls, got %d INFO and %d WARN:\n%s",
			checksInfo, checksWarn, logs.String())
	}
	if requestsWarn != 3 {
		t.Errorf("review_requests_unreadable: want a WARN per poll (3), got %d:\n%s", requestsWarn, logs.String())
	}
}

// A poll that started with one token and finished after another was set
// stores nothing: its PRs belong to the old login, and the next poll, with
// the new token, would otherwise be served them first.
func TestRefreshLoop_DropsAPollWhoseTokenWasReplaced(t *testing.T) {
	logger := testLogger(t)
	store := storage.NewStorage(logger, t.TempDir()+"/elly.db")
	if err := store.StorePAT("ghp_old", "me", time.Time{}); err != nil {
		t.Fatal(err)
	}
	var swapped atomic.Bool
	seen := make(chan int, 1)
	githubAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Authorization") {
		case "bearer ghp_old":
			if swapped.CompareAndSwap(false, true) {
				if err := store.StorePAT("ghp_new", "other", time.Time{}); err != nil {
					t.Error(err)
				}
			}
		case "bearer ghp_new":
			select {
			case seen <- len(store.Prs().Prs):
			default:
			}
		}
		_, _ = w.Write([]byte(`{"data":{"search":{"edges":[{"node":{
			"id":"PR_1","url":"https://github.com/o/r/pull/1","title":"t","author":{"login":"me"},
			"updatedAt":"2026-09-08T10:00:00Z","repository":{"url":"https://github.com/o/r","name":"r","owner":{"login":"o"}},
			"reviewThreads":{"totalCount":0,"pageInfo":{"hasNextPage":false},"edges":[]}
		}}]}}}`))
	}))
	defer githubAPI.Close()
	tracker := backoff.New(logger, time.Millisecond)
	defer tracker.Stop()
	go startRefreshLoop(store, tracker, githubAPI.URL, nil, logger)

	select {
	case n := <-seen:
		if n != 0 {
			t.Errorf("the new token's first poll found %d PRs fetched with the old one", n)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no poll with the new token")
	}
}

// -github-api points a throwaway elly at a stub; anything but GitHub or a
// loopback address would hand the stored token to whoever runs it.
func TestGithubAPIFlag(t *testing.T) {
	for raw, ok := range map[string]bool{
		github.DefaultAPIURL:         true,
		"http://127.0.0.1:5000":      true,
		"http://localhost:5000/":     true,
		"http://[::1]:5000":          true,
		"https://api.github.example": false,
		"http://10.0.0.1:5000":       false,
		"http://localhost.evil.test": false,
		"ftp://127.0.0.1:5000":       false,
		"127.0.0.1:5000":             false,
	} {
		got, err := githubAPI(raw)
		if (err == nil) != ok {
			t.Errorf("%q: %q, %v", raw, got, err)
		}
		if ok && strings.HasSuffix(got, "/") {
			t.Errorf("%q: %q keeps its trailing slash, and paths are appended to it", raw, got)
		}
	}
}

// "poll now" (the r key, the button, and the refresh after a PAT PUT) polls
// even when the last poll is younger than the interval; a timer-driven tick,
// the one at startup included, still skips then.
func TestRefreshLoop_ManualRefreshPollsWithinTheInterval(t *testing.T) {
	logger := testLogger(t)
	store := storage.NewStorage(logger, t.TempDir()+"/elly.db")
	if err := store.StorePAT("ghp_x", "me", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := store.StoreRepoPrs(nil); err != nil { // polled just now
		t.Fatal(err)
	}
	var calls atomic.Int32
	githubAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"data":{"search":{"edges":[]}}}`))
	}))
	defer githubAPI.Close()
	tracker := backoff.New(logger, time.Hour)
	defer tracker.Stop()
	go startRefreshLoop(store, tracker, githubAPI.URL, nil, logger)

	time.Sleep(100 * time.Millisecond)
	if n := calls.Load(); n != 0 {
		t.Fatalf("the startup tick polled %d times within the interval", n)
	}
	tracker.RequestRefresh()
	waitFor(t, "a poll on request", func() bool { return calls.Load() > 0 })
}

func TestRestrictDBFilesLeavesOnlyTheOwnerAccess(t *testing.T) {
	db := filepath.Join(t.TempDir(), "elly.db")
	for _, f := range []string{db, db + "-wal"} {
		if err := os.WriteFile(f, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := restrictDBFiles(db); err != nil {
		t.Fatalf("a missing -shm is not an error: %v", err)
	}
	for _, f := range []string{db, db + "-wal"} {
		if fi, err := os.Stat(f); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v %v", filepath.Base(f), fi.Mode().Perm(), err)
		}
	}
}

// A token stored before elly read its expiry, or whose expiry was lost, gets
// it back at startup from the header GitHub sends with every answer. An
// expiry already stored is not asked for again.
func TestInitPAT_BackfillsMissingExpiries(t *testing.T) {
	expiry := map[string]string{
		"bearer github_pat_main": "2026-11-22 10:00:00 +0100",
		"bearer github_pat_acme": "2027-01-01 00:00:00 +0000",
	}
	var askedKnown atomic.Bool
	githubAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth == "bearer github_pat_known" || auth == "bearer github_pat_dead" {
			askedKnown.Store(true)
		}
		if e, ok := expiry[auth]; ok {
			w.Header().Set("Github-Authentication-Token-Expiration", e)
		}
		var body bytes.Buffer
		_, _ = body.ReadFrom(r.Body)
		if strings.Contains(body.String(), "viewer") {
			w.Write(graphqlViewerResponse("me"))
			return
		}
		w.Write(graphqlSearchResponse())
	}))
	defer githubAPI.Close()

	logger := testLogger(t)
	store := storage.NewStorage(logger, filepath.Join(t.TempDir(), "elly.db"))
	if err := store.StorePAT("github_pat_main", "me", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutOwnerPAT("acme", "github_pat_acme", "me", time.Time{}); err != nil {
		t.Fatal(err)
	}
	known := time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC)
	if err := store.PutOwnerPAT("known", "github_pat_known", "me", known); err != nil {
		t.Fatal(err)
	}
	if err := store.PutOwnerPAT("dead", "github_pat_dead", "me", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetOwnerPollState(storage.OwnerPAT{Owner: "dead", Token: "github_pat_dead", Refused: "owner_token_refused"}); err != nil {
		t.Fatal(err)
	}

	if setup, err := initPAT(store, githubAPI.URL, logger); err != nil || setup {
		t.Fatalf("initPAT = %v, %v", setup, err)
	}

	pat, _, _ := store.GetPAT()
	if want := time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC); !pat.ExpiresAt.Equal(want) {
		t.Errorf("main token expires %v, want %v", pat.ExpiresAt, want)
	}
	owners, err := store.OwnerPATs()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]time.Time{}
	for _, o := range owners {
		got[o.Owner] = o.ExpiresAt
	}
	if want := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC); !got["acme"].Equal(want) {
		t.Errorf("acme expires %v, want %v", got["acme"], want)
	}
	if !got["known"].Equal(known) {
		t.Errorf("known expires %v, want it kept at %v", got["known"], known)
	}
	if askedKnown.Load() {
		t.Error("asked GitHub for an expiry already stored, or with a refused token")
	}
}
