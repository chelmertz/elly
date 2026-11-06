package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chelmertz/elly/internal/storage"
	"github.com/chelmertz/elly/internal/types"
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
		`dead-org · <span class="warn">refused</span>`, `data-owner="acme"`,
		// The sidebar line warns too, so a dying token is seen without opening the dialog.
		`<p class="tokens-line">Tokens · <span class="warn">acme expires in 2 d</span> · <span class="warn">dead-org refused</span> · <button`} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %s", want)

		}
	}
	if strings.Contains(page, "secret") {
		t.Error("the page carries a token")
	}
}

// Every token is a row of one list: the main token says it covers every
// other org, its form stays folded until Replace, and an org token is added
// below the list.
func TestThePageListsEveryTokenAsARow(t *testing.T) {
	w := newRoutes(t, "")
	if err := w.store.StorePAT("github_pat_main", "me", time.Now().Add(2*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	page := w.do("GET", "/", "").Body.String()
	for _, want := range []string{`<p class="tokens-line">Tokens · <span class="warn">main expires in 2 d</span> · <button`,
		`<li><span>@me · main · <span class="warn">expires in 2 d</span></span>`, "covers every org without its own token",
		`<form class="pat-form" hidden>`, `class="replace-pat"`, "Add an org token"} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %s", want)
		}
	}
	if err := w.store.PutOwnerPAT("acme", "github_pat_acmesecret", "me", time.Time{}); err != nil {
		t.Fatal(err)
	}
	page = w.do("GET", "/", "").Body.String()
	if !strings.Contains(page, "<li><span>acme</span>") {
		t.Error("the page does not list the stored org token as a row")
	}
}

func storageOwner(owner, token, refused string) storage.OwnerPAT {
	return storage.OwnerPAT{Owner: owner, Token: token, Refused: refused}
}

// The status names the owner only on an owner token's degradation, says
// "refused" only for a refused token, and a healthy store logs nothing.
func TestStatusNamesOwnersOnlyWhereTheyApply(t *testing.T) {
	var logs strings.Builder
	w := newRoutesLogging(t, "", slog.New(slog.NewTextHandler(&logs, nil)))
	if err := w.store.StorePAT("github_pat_main", "me", time.Time{}); err != nil {
		t.Fatal(err)
	}
	for _, o := range []string{"acme", "dead-org"} {
		if err := w.store.PutOwnerPAT(o, "github_pat_"+o, "me", time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.store.SetOwnerPollState(storageOwner("dead-org", "github_pat_dead-org", "owner_token_refused")); err != nil {
		t.Fatal(err)
	}
	if err := w.store.StoreDegradations([]types.Degradation{{Kind: "main_kind"}, {Kind: "acme_kind", Owner: "acme"}}); err != nil {
		t.Fatal(err)
	}
	var status struct {
		Degradations []map[string]string `json:"degradations"`
		OwnerTokens  []map[string]string `json:"owner_tokens"`
	}
	if err := json.Unmarshal(w.do("GET", "/api/v0/config/status", "").Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	owners := map[string]string{}
	for _, d := range status.Degradations {
		owner, ok := d["owner"]
		if ok != (d["kind"] == "acme_kind") {
			t.Errorf("degradation %v", d)
		}
		owners[d["kind"]] = owner
	}
	if owners["acme_kind"] != "acme" {
		t.Errorf("owners %v", owners)
	}
	for _, o := range status.OwnerTokens {
		if _, refused := o["refused"]; refused != (o["owner"] == "dead-org") {
			t.Errorf("owner token %v", o)
		}
	}
	if len(status.OwnerTokens) != 2 {
		t.Errorf("owner tokens %v", status.OwnerTokens)
	}
	w.do("GET", "/", "")
	if strings.Contains(logs.String(), "could not list owner tokens") {
		t.Errorf("a healthy store logged a failure: %s", logs.String())
	}
}
