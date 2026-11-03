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
	mux     *http.ServeMux
	store   *storage.DbStorage
	tracker *backoff.Tracker
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
	tracker := backoff.New(logger, time.Minute)
	t.Cleanup(tracker.Stop)
	cfg := HttpServerConfig{Store: store, Logger: logger, Tracker: tracker, GithubURL: githubURL}
	return routesWorld{mux: routes(cfg), store: store, tracker: tracker}
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

// turn=you is what the page counts as on the user, so the bar and the page
// agree: points alone keep an untouched own PR counted forever.
func TestGetPrsTurnYouMatchesThePage(t *testing.T) {
	w := newRoutes(t, "")
	if err := w.store.StorePAT("ghp_x", "me", time.Time{}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := w.store.StoreRepoPrs([]types.ViewPr{
		{Url: "https://github.com/o/r/pull/1", Title: "asked", Author: "a", ReviewRequestedFromUsers: []string{"me"}, LastUpdated: now, RawJsonResponse: []byte("{}")},
		{Url: "https://github.com/o/r/pull/2", Title: "iced own", Author: "me", Additions: 10, LastUpdated: now.Add(-30 * 24 * time.Hour), RawJsonResponse: []byte("{}")},
		{Url: "https://github.com/o/r/pull/3", Title: "someone's", Author: "a", Additions: 10, LastUpdated: now, RawJsonResponse: []byte("{}")},
	}); err != nil {
		t.Fatal(err)
	}
	var prs []types.ViewPr
	if err := json.NewDecoder(w.do("GET", "/api/v0/prs?turn=you", "").Body).Decode(&prs); err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, p := range prs {
		titles = append(titles, p.Title)
	}
	if strings.Join(titles, ",") != "asked" {
		t.Errorf("turn=you: got %q, want only the PR asked of the user", titles)
	}
	page := w.do("GET", "/", "").Body.String()
	if !strings.Contains(page, `<p class="stats">1 on you`) {
		t.Error("the page does not count the same one PR on the user")
	}
}

func TestRootIsTheTurnPage(t *testing.T) {
	w := newRoutes(t, "")
	page := w.do("GET", "/", "").Body.String()
	if !strings.Contains(page, `id="pr-o-r-1"`) || strings.Contains(page, "classic page") {
		t.Error("/ does not serve the page grouped by turn")
	}
	if rec := w.do("GET", "/next", ""); rec.Code != http.StatusFound || rec.Header().Get("Location") != "/" {
		t.Errorf("/next: %d to %q, want a redirect to /", rec.Code, rec.Header().Get("Location"))
	}
	if rec := w.do("GET", "/settings", ""); rec.Code != http.StatusNotFound {
		t.Errorf("an unknown path: %d, want 404", rec.Code)
	}
	if !strings.Contains(page, `<button type="button" class="poll">`) {
		t.Error("the page has no way to poll now")
	}
}

// Without a token the page opens the token form, since nothing else works
// until one is set; with one it names the account and warns near expiry.
func TestThePageSetsAndShowsTheToken(t *testing.T) {
	w := newRoutes(t, "")
	page := w.do("GET", "/", "").Body.String()
	for _, want := range []string{`<details class="token" open>`, `<form class="pat-form">`, "No token yet"} {
		if !strings.Contains(page, want) {
			t.Errorf("setup mode: page lacks %s", want)
		}
	}
	if strings.Contains(page, `class="clear-pat"`) {
		t.Error("setup mode offers to clear a token that is not there")
	}

	if err := w.store.StorePAT("ghp_x", "me", time.Now().Add(3*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	page = w.do("GET", "/", "").Body.String()
	for _, want := range []string{`<details class="token">`, "@me", `class="clear-pat"`, `<span class="warn">expires in 3 d</span>`} {
		if !strings.Contains(page, want) {
			t.Errorf("with a token: page lacks %s", want)
		}
	}
}

// Clearing the token idles the poll loop rather than ending it, so a token
// set later is polled; and the PRs the old token fetched stop being served.
func TestClearingTheTokenKeepsThePollLoop(t *testing.T) {
	w := newRoutes(t, "")
	if err := w.store.StorePAT("ghp_old", "me", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if !w.tracker.Tick() {
		t.Fatal("no first tick")
	}
	if rec := w.do("DELETE", "/api/v0/config/pat", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", rec.Code)
	}
	if n := len(w.store.Prs().Prs); n != 0 {
		t.Errorf("%d PRs still served after the token was cleared", n)
	}
	w.tracker.RequestRefresh()
	ticked := make(chan bool, 1)
	go func() { ticked <- w.tracker.Tick() }()
	select {
	case ok := <-ticked:
		if !ok {
			t.Error("clearing the token ended the poll loop")
		}
	case <-time.After(5 * time.Second):
		t.Error("no tick after clearing the token")
	}
}
