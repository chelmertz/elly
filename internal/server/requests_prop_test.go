package server

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chelmertz/elly/internal/backoff"
	"github.com/chelmertz/elly/internal/storage"
	"github.com/chelmertz/elly/internal/types"
	"pgregory.net/rapid"
)

const fuzzAnnotationToken = "fuzz-token"

// fuzzWorld is elly with seeded PRs, a token, a bury, an annotation and
// notification history, behind the real guard.
func fuzzWorld(t *testing.T, githubURL string) (http.Handler, *storage.DbStorage) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store := storage.NewStorage(logger, t.TempDir()+"/elly.db")
	now := time.Now()
	if err := store.StorePAT("ghp_seeded", "me", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := store.StoreRepoPrs([]types.ViewPr{
		{Url: "https://github.com/o/r/pull/1", Title: "one", Author: "someone", ReviewRequestedFromUsers: []string{"me"}, LastUpdated: now, RawJsonResponse: []byte("{}")},
		{Url: "https://github.com/o/r/pull/2", Title: "two", Author: "me", LastUpdated: now, RawJsonResponse: []byte("{}")},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Bury("https://github.com/o/r/pull/2"); err != nil {
		t.Fatal(err)
	}
	if err := store.PutAnnotation("https://github.com/o/r/pull/1", types.Annotation{Source: "claude-review", State: types.AnnotationRunning, Links: []types.AnnotationLink{}}, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := store.StoreNotifySeen(map[string][]string{"https://github.com/o/r/pull/1": {"review_requested"}}); err != nil {
		t.Fatal(err)
	}
	tracker := backoff.New(logger, time.Hour)
	t.Cleanup(tracker.Stop)
	cfg := HttpServerConfig{Store: store, Logger: logger, Tracker: tracker, GithubURL: githubURL,
		AnnotationToken: fuzzAnnotationToken, AnnotationSchemes: []string{"claude-review"}, ReviewRepos: []string{"o/*"}}
	return guard("localhost:9876", nil, routes(cfg)), store
}

// state is everything a request could change: PR rows with their buries and
// annotations, the token, and the notification history.
func state(t *testing.T, store *storage.DbStorage) string {
	pat, found, err := store.GetPAT()
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal([]any{store.Prs().Prs, pat.Token, pat.Username, found, store.NotifySeen()})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func drawRequest(t *rapid.T) (*http.Request, bool) {
	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	id := rapid.OneOf(
		rapid.Just(b64("https://github.com/o/r/pull/1")),
		rapid.Just(b64("https://github.com/o/r/pull/2")),
		rapid.Map(rapid.String(), b64),
		rapid.StringMatching(`[A-Za-z0-9+/=%@.-]{0,12}`),
	).Draw(t, "id")
	path := rapid.OneOf(
		rapid.SampledFrom([]string{"/", "/next", "/health", "/metrics", "/api/v0/prs", "/api/v0/prs?turn=you",
			"/api/v0/prs/refresh", "/api/v0/config/pat", "/api/v0/config/status", "/favicon.ico", "/api/v0/prs/"}),
		rapid.Map(rapid.IntRange(-2000, 2000), func(n int) string { return "/api/v0/prs?minPoints=" + strconv.Itoa(n) }),
		rapid.Map(rapid.SampledFrom([]string{"bury", "unbury", "delete", "annotations", ""}), func(a string) string { return "/api/v0/prs/" + id + "/" + a }),
		rapid.Map(rapid.StringMatching(`[a-z0-9-]{0,45}`), func(s string) string { return "/api/v0/prs/" + id + "/annotations/" + s }),
		rapid.Map(rapid.StringMatching(`[a-zA-Z0-9/._~-]{0,30}`), func(s string) string { return "/" + s }),
	).Draw(t, "path")
	method := rapid.SampledFrom([]string{"GET", "HEAD", "OPTIONS", "POST", "PUT", "DELETE", "PATCH"}).Draw(t, "method")
	body := rapid.OneOf(
		rapid.Just(""),
		rapid.Just(`{"token":"ghp_new"}`),
		rapid.Just(`{"state":"needs_you","note":"n","links":[],"expiresAt":"`+time.Now().Add(time.Hour).UTC().Format(time.RFC3339)+`"}`),
		rapid.Just(`{"state":"handled","links":[{"label":"x","url":"javascript:alert(1)"}],"expiresAt":"2000-01-01T00:00:00Z"}`),
		rapid.String(),
	).Draw(t, "body")

	r := httptest.NewRequest(method, "http://localhost:9876/", strings.NewReader(body))
	r.URL.RawQuery = ""
	r.RequestURI = ""
	if i := strings.IndexByte(path, '?'); i >= 0 {
		r.URL.Path, r.URL.RawQuery = path[:i], path[i+1:]
	} else {
		r.URL.Path = path
	}
	host := rapid.SampledFrom([]string{"localhost:9876", "127.0.0.1:9876", "[::1]:9876", "attacker.example:9876", "localhost:1234"}).Draw(t, "host")
	r.Host = host
	origin := rapid.SampledFrom([]string{"", "http://localhost:9876", "http://127.0.0.1:9876", "https://evil.example", "null", "http://localhost:1234"}).Draw(t, "origin")
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	site := rapid.SampledFrom([]string{"", "same-origin", "none", "same-site", "cross-site"}).Draw(t, "Sec-Fetch-Site")
	if site != "" {
		r.Header.Set("Sec-Fetch-Site", site)
	}
	if ct := rapid.SampledFrom([]string{"", "application/json", "text/plain", "application/x-www-form-urlencoded"}).Draw(t, "Content-Type"); ct != "" {
		r.Header.Set("Content-Type", ct)
	}
	if auth := rapid.SampledFrom([]string{"", "Bearer " + fuzzAnnotationToken, "Bearer wrong"}).Draw(t, "Authorization"); auth != "" {
		r.Header.Set("Authorization", auth)
	}
	foreign := strings.HasPrefix(host, "attacker.") || strings.HasSuffix(host, ":1234") ||
		(isWrite(method) && (origin == "https://evil.example" || origin == "null" || origin == "http://localhost:1234" || site == "same-site" || site == "cross-site"))
	return r, foreign
}

// Random requests at the real handler: none answers 5xx, every response has
// the base headers, no GET (or HEAD, OPTIONS) changes stored state, and no
// cross-site or rebound request succeeds or changes anything.
func TestPropRequestsAtTheRealHandler(t *testing.T) {
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer gh.Close()
	rapid.Check(t, func(rt *rapid.T) {
		handler, store := fuzzWorld(t, gh.URL)
		for i, n := 0, rapid.IntRange(1, 4).Draw(rt, "requests"); i < n; i++ {
			r, foreign := drawRequest(rt)
			before := state(t, store)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			after := state(t, store)
			what := r.Method + " " + r.URL.String() + " Host " + r.Host
			if w.Code >= 500 {
				rt.Fatalf("%s: %d %q", what, w.Code, w.Body)
			}
			for h, v := range map[string]string{"X-Frame-Options": "DENY", "X-Content-Type-Options": "nosniff", "Cross-Origin-Resource-Policy": "same-origin", "Content-Security-Policy": contentSecurityPolicy} {
				if got := w.Header().Get(h); got != v {
					rt.Fatalf("%s (%d): %s %q", what, w.Code, h, got)
				}
			}
			if !isWrite(r.Method) && before != after {
				rt.Fatalf("%s changed stored state:\nbefore %s\nafter  %s", what, before, after)
			}
			if foreign && (w.Code < 400 || before != after) {
				rt.Fatalf("%s from Origin %q, Sec-Fetch-Site %q got %d, state changed %v", what, r.Header.Get("Origin"), r.Header.Get("Sec-Fetch-Site"), w.Code, before != after)
			}
		}
	})
}
