package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chelmertz/elly/internal/storage"
)

// ownerGithub answers the login check as login, any search with nothing, and
// the owner probe with probe.
func ownerGithub(t *testing.T, login, probe string, calls *atomic.Int32) string {
	t.Helper()
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		switch {
		case strings.Contains(string(body), "viewer { login }"):
			_, _ = w.Write([]byte(`{"data":{"viewer":{"login":"` + login + `"}}}`))
		case strings.Contains(string(body), "repositoryOwner"):
			_, _ = w.Write([]byte(probe))
		default:
			_, _ = w.Write([]byte(`{"data":{"search":{"edges":[]}}}`))
		}
	}))
	t.Cleanup(gh.Close)
	return gh.URL
}

const reachesAcme = `{"data":{"repositoryOwner":{"login":"Acme","repositories":{"totalCount":2}}}}`

func TestOwnerPatPutStoresUnderGitHubsSpelling(t *testing.T) {
	var calls atomic.Int32
	w := newRoutes(t, ownerGithub(t, "me", reachesAcme, &calls))
	if err := w.store.StorePAT("github_pat_main", "me", time.Time{}); err != nil {
		t.Fatal(err)
	}
	const token = "github_pat_acmesecret"
	rec := w.do("PUT", "/api/v0/config/pat/acme", `{"token":"`+token+`"}`)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), token) {
		t.Fatalf("%d %q", rec.Code, rec.Body)
	}
	owners, _ := w.store.OwnerPATs()
	if len(owners) != 1 || owners[0].Owner != "Acme" || owners[0].Token != token {
		t.Fatalf("%+v", owners)
	}
	status := w.do("GET", "/api/v0/config/status", "").Body.String()
	if !strings.Contains(status, `"owner":"Acme"`) || strings.Contains(status, token) {
		t.Errorf("status %s", status)
	}
	if rec := w.do("DELETE", "/api/v0/config/pat/ACME", ""); rec.Code != http.StatusNoContent {
		t.Errorf("DELETE: %d", rec.Code)
	}
	if rec := w.do("DELETE", "/api/v0/config/pat/acme", ""); rec.Code != http.StatusNotFound {
		t.Errorf("second DELETE: %d", rec.Code)
	}
}

func TestOwnerPatPutRefuses(t *testing.T) {
	noAccess := `{"data":{"repositoryOwner":{"login":"acme","repositories":{"totalCount":0}}}}`
	for _, c := range []struct {
		name, login, probe, owner, token string
		primary                          bool
		want                             int
		wantBody                         string
		noGithub                         bool
	}{
		{"a classic token", "me", reachesAcme, "acme", "ghp_classic", true, http.StatusBadRequest, "fine-grained", true},
		{"not an owner name", "me", reachesAcme, "-acme", "github_pat_x", true, http.StatusBadRequest, "owner name", true},
		{"a token that cannot see the owner", "me", noAccess, "acme", "github_pat_x", true, http.StatusBadRequest, "approval still pending", false},
		{"no main token", "me", reachesAcme, "acme", "github_pat_x", false, http.StatusConflict, "main token first", false},
		{"another login", "someone", reachesAcme, "acme", "github_pat_x", true, http.StatusBadRequest, "another login", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			var calls atomic.Int32
			w := newRoutes(t, ownerGithub(t, c.login, c.probe, &calls))
			if c.primary {
				if err := w.store.StorePAT("github_pat_main", "me", time.Time{}); err != nil {
					t.Fatal(err)
				}
			}
			rec := w.do("PUT", "/api/v0/config/pat/"+c.owner, `{"token":"`+c.token+`"}`)
			if rec.Code != c.want || !strings.Contains(rec.Body.String(), c.wantBody) {
				t.Errorf("%d %q, want %d %q", rec.Code, rec.Body, c.want, c.wantBody)
			}
			if owners, _ := w.store.OwnerPATs(); len(owners) != 0 {
				t.Errorf("stored %+v", owners)
			}
			if c.noGithub && calls.Load() != 0 {
				t.Errorf("sent to GitHub %d times", calls.Load())
			}
		})
	}
}

// Removing an owner's token while a new one for it is being checked wins, as
// clearing the main token does.
func TestOwnerPatDeleteDuringValidationWins(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var once atomic.Bool
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch {
		case strings.Contains(string(body), "repositoryOwner"):
			if once.CompareAndSwap(false, true) {
				close(entered)
				<-release
			}
			_, _ = w.Write([]byte(reachesAcme))
		case strings.Contains(string(body), "viewer { login }"):
			_, _ = w.Write([]byte(`{"data":{"viewer":{"login":"me"}}}`))
		default:
			_, _ = w.Write([]byte(`{"data":{"search":{"edges":[]}}}`))
		}
	}))
	defer gh.Close()
	w := newRoutes(t, gh.URL)
	if err := w.store.StorePAT("github_pat_main", "me", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := w.store.PutOwnerPAT("Acme", "github_pat_old", "me", time.Time{}); err != nil {
		t.Fatal(err)
	}
	put := make(chan int)
	go func() { put <- w.do("PUT", "/api/v0/config/pat/acme", `{"token":"github_pat_new"}`).Code }()
	<-entered
	if rec := w.do("DELETE", "/api/v0/config/pat/Acme", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE: %d", rec.Code)
	}
	close(release)
	if code := <-put; code != http.StatusConflict {
		t.Errorf("the PUT that lost: %d", code)
	}
	if owners, _ := w.store.OwnerPATs(); len(owners) != 0 {
		t.Errorf("a token came back: %+v", owners)
	}
}

// The page lists each org token by owner, says when one was refused or is
// about to expire, and offers the form only once a main token exists.
func TestThePageListsOwnerTokens(t *testing.T) {
	w := newRoutes(t, "")
	if page := w.do("GET", "/", "").Body.String(); strings.Contains(page, `class="owner-form"`) {
		t.Error("setup mode offers org tokens before a main token")
	}
	if err := w.store.StorePAT("github_pat_main", "me", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := w.store.PutOwnerPAT("acme", "github_pat_acmesecret", "me", time.Now().Add(2*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := w.store.PutOwnerPAT("dead-org", "github_pat_deadsecret", "me", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := w.store.SetOwnerPollState(storageOwner("dead-org", "github_pat_deadsecret", "owner_token_refused")); err != nil {
		t.Fatal(err)
	}
	page := w.do("GET", "/", "").Body.String()
	for _, want := range []string{`class="owner-form"`, "made by @me", `acme · <span class="warn">expires in 2 d</span>`,
		`dead-org · <span class="warn">refused</span>`, `data-owner="acme"`} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %s", want)

		}
	}
	if strings.Contains(page, "secret") {
		t.Error("the page carries a token")
	}
}

func storageOwner(owner, token, refused string) storage.OwnerPAT {
	return storage.OwnerPAT{Owner: owner, Token: token, Refused: refused}
}
