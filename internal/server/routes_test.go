package server

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chelmertz/elly/internal/backoff"
	"github.com/chelmertz/elly/internal/storage"
	"github.com/chelmertz/elly/internal/types"
)

type routesWorld struct {
	mux   *http.ServeMux
	store *storage.DbStorage
}

func newRoutes(t *testing.T, githubURL string) routesWorld {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store := storage.NewStorage(logger, filepath.Join(t.TempDir(), "elly.db"))
	now := time.Now()
	prs := []types.ViewPr{
		// Someone else's small PR: 50 points.
		{Url: "https://github.com/o/r/pull/1", Title: "small one", Author: "someone", Additions: 10, LastUpdated: now, RawJsonResponse: []byte("{}")},
		// Someone else's approved PR: -100 points.
		{Url: "https://github.com/o/r/pull/2", Title: "approved one", Author: "someone", ReviewStatus: "APPROVED", LastUpdated: now, RawJsonResponse: []byte("{}")},
	}
	if err := store.StoreRepoPrs(prs); err != nil {
		t.Fatal(err)
	}
	cfg := HttpServerConfig{Store: store, Logger: logger, Tracker: backoff.New(logger, time.Minute), GithubURL: githubURL}
	return routesWorld{mux: routes(cfg), store: store}
}

func (w routesWorld) do(method, url, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, url, strings.NewReader(body))
	rec := httptest.NewRecorder()
	w.mux.ServeHTTP(rec, r)
	return rec
}

func TestGetPrsMinPoints(t *testing.T) {
	w := newRoutes(t, "")
	titles := func(url string) string {
		rec := w.do("GET", url, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d", url, rec.Code)
		}
		var prs []types.ViewPr
		if err := json.NewDecoder(rec.Body).Decode(&prs); err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, p := range prs {
			out = append(out, p.Title)
		}
		return strings.Join(out, ",")
	}
	cases := map[string]string{
		"/api/v0/prs":                "small one,approved one",
		"/api/v0/prs?minPoints=1":    "small one",
		"/api/v0/prs?minPoints=50":   "small one",
		"/api/v0/prs?minPoints=51":   "",
		"/api/v0/prs?minPoints=-100": "small one,approved one",
		"/api/v0/prs?minPoints=x":    "small one,approved one",
		"/api/v0/prs?minPoints=1000": "small one,approved one",
	}
	for url, want := range cases {
		if got := titles(url); got != want {
			t.Errorf("%s: got %q, want %q", url, got, want)
		}
	}
}

func TestBuryRoute(t *testing.T) {
	w := newRoutes(t, "")
	id := base64.StdEncoding.EncodeToString([]byte("https://github.com/o/r/pull/1"))
	buried := func() bool {
		for _, p := range w.store.Prs().Prs {
			if p.Url == "https://github.com/o/r/pull/1" {
				return p.Buried
			}
		}
		return false
	}
	if rec := w.do("POST", "/api/v0/prs/"+id+"/bury", ""); rec.Code != http.StatusNoContent || !buried() {
		t.Fatalf("bury: %d, buried %v", rec.Code, buried())
	}
	if rec := w.do("POST", "/api/v0/prs/"+id+"/unbury", ""); rec.Code != http.StatusNoContent || buried() {
		t.Fatalf("unbury: %d, buried %v", rec.Code, buried())
	}
	if rec := w.do("POST", "/api/v0/prs/"+id+"/delete", ""); rec.Code != http.StatusNotFound {
		t.Errorf("unknown action: %d", rec.Code)
	}
	if rec := w.do("POST", "/api/v0/prs/@@@/bury", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("undecodable PR id: %d", rec.Code)
	}
}

func TestPatRoutesRefuseBadInput(t *testing.T) {
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer gh.Close()
	w := newRoutes(t, gh.URL)
	for body, want := range map[string]string{
		"not json":          "invalid JSON",
		`{}`:                "token is required",
		`{"token":"ghp_x"}`: "invalid token",
	} {
		rec := w.do("PUT", "/api/v0/config/pat", body)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s: %d %q", body, rec.Code, rec.Body)
		}
	}
	if _, found, _ := w.store.GetPAT(); found {
		t.Error("a refused token was stored")
	}
	if err := w.store.StorePAT("ghp_old", "me", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if rec := w.do("DELETE", "/api/v0/config/pat", ""); rec.Code != http.StatusNoContent {
		t.Errorf("delete: %d", rec.Code)
	}
	if _, found, _ := w.store.GetPAT(); found {
		t.Error("the token survived DELETE")
	}
}

func TestPageAndHealth(t *testing.T) {
	w := newRoutes(t, "")
	if rec := w.do("GET", "/health", ""); rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Errorf("health: %d %q", rec.Code, rec.Body)
	}
	rec := w.do("GET", "/", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "small one") {
		t.Errorf("page: %d, has the PR: %v", rec.Code, strings.Contains(rec.Body.String(), "small one"))
	}
}

// A fine-grained token can never be granted the Checks permission, so
// "checks_unreadable" is the normal state of such a setup rather than a fault:
// it stays in the API, where the pr-status skill reads it, but a warning on
// every page view about something nobody can fix was noise. Any other
// degradation still shows on the page.
func TestPageHidesTokenLimitationsTheApiStillReports(t *testing.T) {
	w := newRoutes(t, "")
	if err := w.store.StorePAT("ghp_x", "me", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := w.store.StoreDegradations([]types.Degradation{
		{Kind: "checks_unreadable", Message: "checks-message", Remedy: "checks-remedy"},
		{Kind: "review_requests_unreadable", Message: "requests-message", Remedy: "requests-remedy"},
	}); err != nil {
		t.Fatal(err)
	}

	page := w.do("GET", "/", "").Body.String()
	for _, hidden := range []string{"checks-message", "checks-remedy"} {
		if strings.Contains(page, hidden) {
			t.Errorf("the page shows %q, a token limitation rather than a fault", hidden)
		}
	}
	for _, shown := range []string{"requests-message", "requests-remedy"} {
		if !strings.Contains(page, shown) {
			t.Errorf("the page lost %q, a real degradation", shown)
		}
	}

	var status struct {
		Degradations []map[string]string `json:"degradations"`
	}
	if err := json.NewDecoder(w.do("GET", "/api/v0/config/status", "").Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, d := range status.Degradations {
		kinds = append(kinds, d["kind"])
	}
	if got := strings.Join(kinds, ","); got != "checks_unreadable,review_requests_unreadable" {
		t.Errorf("the API must keep reporting every degradation, got kinds %q", got)
	}
}
