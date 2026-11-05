package main

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/chelmertz/elly/internal/backoff"
	"github.com/chelmertz/elly/internal/github"
	"github.com/chelmertz/elly/internal/storage"
	"github.com/chelmertz/elly/internal/types"
)

var mergeNow = time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)

func prIn(owner string, n int) types.ViewPr {
	return types.ViewPr{Url: fmt.Sprintf("https://github.com/%s/r/pull/%d", owner, n), RepoOwner: owner}
}

func urls(prs []types.ViewPr) []string {
	var out []string
	for _, pr := range prs {
		out = append(out, pr.Url)
	}
	slices.Sort(out)
	return out
}

func kinds(ds []types.Degradation) []string {
	var out []string
	for _, d := range ds {
		out = append(out, d.Kind+"/"+d.Owner)
	}
	return out
}

// answers is a fake owner poll: each owner's result, or its error.
type answers map[string]struct {
	prs []types.ViewPr
	err error
}

func (a answers) poll(calls *[]string) func(o storage.OwnerPAT) ([]types.ViewPr, []types.Degradation, error) {
	return func(o storage.OwnerPAT) ([]types.ViewPr, []types.Degradation, error) {
		*calls = append(*calls, o.Owner)
		r := a[o.Owner]
		return r.prs, nil, r.err
	}
}

func merge(primary []types.ViewPr, owners []storage.OwnerPAT, previous []types.ViewPr, a answers) ([]types.ViewPr, []types.Degradation, []storage.OwnerPAT, []string) {
	var calls []string
	prs, ds, states := mergeOwners(primary, "me", owners, previous, mergeNow, a.poll(&calls), slog.New(slog.NewTextHandler(io.Discard, nil)))
	return prs, ds, states, calls
}

func TestMergeOwnersTakesEachOwnerFromItsToken(t *testing.T) {
	owners := []storage.OwnerPAT{{Owner: "acme", Token: "github_pat_acme", Username: "Me"}}
	// the primary also sees a public acme PR; the owner token answers acme
	primary := []types.ViewPr{prIn("mine", 1), prIn("acme", 9)}
	prs, ds, states, _ := merge(primary, owners, nil, answers{"acme": {prs: []types.ViewPr{prIn("acme", 2)}}})
	if got, want := urls(prs), urls([]types.ViewPr{prIn("acme", 2), prIn("mine", 1)}); !slices.Equal(got, want) {
		t.Errorf("prs %v, want %v", got, want)
	}
	if len(ds) != 0 {
		t.Errorf("degraded: %v", kinds(ds))
	}
	if len(states) != 1 || !states[0].LastSuccess.Equal(mergeNow) || states[0].Refused != "" {
		t.Errorf("states %+v", states)
	}
}

func TestMergeOwnersDropsARefusedOwnerAndStopsAsking(t *testing.T) {
	previous := []types.ViewPr{prIn("mine", 1), prIn("acme", 2)}
	for _, tc := range []struct {
		err  error
		kind string
	}{
		{fmt.Errorf("x: %w", github.ErrInvalidToken), degradationOwnerRefused},
		{github.ErrNoAccess, degradationOwnerNoAccess},
	} {
		owners := []storage.OwnerPAT{{Owner: "acme", Username: "me", LastSuccess: mergeNow.Add(-time.Minute)}}
		prs, ds, states, _ := merge([]types.ViewPr{prIn("mine", 1)}, owners, previous, answers{"acme": {err: tc.err}})
		if got := urls(prs); !slices.Equal(got, []string{prIn("mine", 1).Url}) {
			t.Errorf("%s: kept %v", tc.kind, got)
		}
		if got := kinds(ds); !slices.Equal(got, []string{tc.kind + "/acme"}) {
			t.Errorf("%s: degradations %v", tc.kind, got)
		}
		if len(states) != 1 || states[0].Refused != tc.kind {
			t.Fatalf("%s: states %+v", tc.kind, states)
		}
		_, ds, _, calls := merge(nil, states, previous, answers{})
		if len(calls) != 0 || !slices.Equal(kinds(ds), []string{tc.kind + "/acme"}) {
			t.Errorf("%s: a refused owner was polled %v, or went quiet %v", tc.kind, calls, kinds(ds))
		}
	}
}

func TestMergeOwnersCarriesAnOwnerThroughABlipForAnHour(t *testing.T) {
	previous := []types.ViewPr{prIn("mine", 1), prIn("acme", 2)}
	blip := answers{"acme": {err: errors.New("network")}}
	for _, tc := range []struct {
		name string
		last time.Time
		kept bool
	}{
		{"a minute after a success", mergeNow.Add(-time.Minute), true},
		{"just under the hour", mergeNow.Add(-ownerCarry + time.Second), true},
		{"at the hour", mergeNow.Add(-ownerCarry), false},
		{"never succeeded", time.Time{}, false},
	} {
		owners := []storage.OwnerPAT{{Owner: "acme", Username: "me", LastSuccess: tc.last}}
		prs, ds, states, _ := merge([]types.ViewPr{prIn("mine", 1)}, owners, previous, blip)
		if got := slices.Contains(urls(prs), prIn("acme", 2).Url); got != tc.kept {
			t.Errorf("%s: kept acme %v", tc.name, got)
		}
		if !slices.Equal(kinds(ds), []string{degradationOwnerUnreachable + "/acme"}) {
			t.Errorf("%s: degradations %v", tc.name, kinds(ds))
		}
		if len(states) != 1 || !states[0].LastSuccess.Equal(tc.last) || states[0].Refused != "" {
			t.Errorf("%s: a blip changed the state %+v", tc.name, states)
		}
	}
}

func TestMergeOwnersHoldsARateLimitedOwnerBack(t *testing.T) {
	until := mergeNow.Add(10 * time.Minute)
	owners := []storage.OwnerPAT{{Owner: "acme", Username: "me", LastSuccess: mergeNow.Add(-time.Minute)}}
	_, ds, states, _ := merge(nil, owners, nil, answers{"acme": {err: &github.ErrRateLimited{UnblockedAt: until}}})
	if len(states) != 1 || !states[0].RateLimitedUntil.Equal(until) {
		t.Fatalf("states %+v", states)
	}
	if !slices.Equal(kinds(ds), []string{degradationOwnerUnreachable + "/acme"}) {
		t.Errorf("degradations %v", kinds(ds))
	}
	previous := []types.ViewPr{prIn("acme", 2)}
	prs, _, _, calls := merge(nil, states, previous, answers{})
	if len(calls) != 0 {
		t.Errorf("polled while rate limited: %v", calls)
	}
	if !slices.Equal(urls(prs), urls(previous)) {
		t.Errorf("a rate limit inside the hour dropped %v", urls(prs))
	}
	if _, _, _, calls := merge(nil, []storage.OwnerPAT{{Owner: "acme", Username: "me", RateLimitedUntil: mergeNow}}, nil, answers{}); len(calls) != 1 {
		t.Errorf("not polled once the limit passed: %v", calls)
	}
}

// A token stored for another login (a race the store refuses, kept here as
// a second guard) is never polled: its PRs would be read as the user's.
func TestMergeOwnersSkipsATokenOfAnotherLogin(t *testing.T) {
	owners := []storage.OwnerPAT{{Owner: "acme", Username: "someone"}}
	prs, ds, _, calls := merge(nil, owners, []types.ViewPr{prIn("acme", 2)}, answers{"acme": {prs: []types.ViewPr{prIn("acme", 3)}}})
	if len(calls) != 0 || len(prs) != 0 {
		t.Errorf("polled %v, kept %v", calls, urls(prs))
	}
	if !slices.Equal(kinds(ds), []string{degradationOwnerOtherLogin + "/acme"}) {
		t.Errorf("degradations %v", kinds(ds))
	}
}

func TestMergeOwnersNeverQuotesGitHub(t *testing.T) {
	owners := []storage.OwnerPAT{{Owner: "acme", Username: "me"}}
	_, ds, _, _ := merge(nil, owners, nil, answers{"acme": {err: errors.New("text from github")}})
	for _, d := range ds {
		if strings.Contains(d.Message+d.Remedy, "text from github") {
			t.Errorf("degradation quotes GitHub: %+v", d)
		}
	}
}

// searchWith is a search answer holding one PR in each owner's repo r.
func searchWith(owners ...string) string {
	var edges []string
	for i, o := range owners {
		edges = append(edges, fmt.Sprintf(`{"node":{"id":"PR_%d","url":"https://github.com/%s/r/pull/%d","title":"t","author":{"login":"me"},
			"updatedAt":"2026-09-08T10:00:00Z","repository":{"url":"https://github.com/%s/r","name":"r","owner":{"login":"%s"}},
			"comments":{"edges":[]},"reviewThreads":{"totalCount":0,"pageInfo":{"hasNextPage":false},"edges":[]}}}`, i+1, o, i+1, o, o))
	}
	return `{"data":{"search":{"edges":[` + strings.Join(edges, ",") + `]}}}`
}

// One poll with a primary and an acme token stores the primary's PRs and
// acme's from its own token, and records acme's success.
func TestRefreshLoopPollsEachOwnerWithItsToken(t *testing.T) {
	githubAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch auth := r.Header.Get("Authorization"); {
		case strings.Contains(string(body), "repositoryOwner") && auth == "bearer github_pat_acme":
			_, _ = w.Write([]byte(`{"data":{"repositoryOwner":{"login":"acme","repositories":{"totalCount":2}}}}`))
		case auth == "bearer github_pat_acme":
			_, _ = w.Write([]byte(searchWith("acme", "public-elsewhere")))
		default:
			_, _ = w.Write([]byte(searchWith("mine")))
		}
	}))
	t.Cleanup(githubAPI.Close)
	var logs syncBuffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	store := storage.NewStorage(logger, filepath.Join(t.TempDir(), "elly.db"))
	if err := store.StorePAT("github_pat_main", "me", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutOwnerPAT("acme", "github_pat_acme", "me", time.Time{}); err != nil {
		t.Fatal(err)
	}
	tracker := backoff.New(logger, time.Hour)
	stored := &signallingStorage{DbStorage: store, stored: make(chan struct{})}
	go startRefreshLoop(stored, tracker, githubAPI.URL, nil, logger)
	select {
	case <-stored.stored:
	case <-time.After(5 * time.Second):
		t.Fatal("the poll was never stored")
	}
	tracker.Stop()
	want := []string{"https://github.com/acme/r/pull/1", "https://github.com/mine/r/pull/1"}
	if got := urls(store.Prs().Prs); !slices.Equal(got, want) {
		t.Errorf("stored %v, want %v", got, want)
	}
	owners, _ := store.OwnerPATs()
	if len(owners) != 1 || owners[0].LastSuccess.IsZero() {
		t.Errorf("acme's success was not recorded: %+v", owners)
	}
	if strings.Contains(logs.String(), "could not store an owner token's poll state") {
		t.Errorf("a stored state logged a failure: %s", logs.String())
	}
}

// signallingStorage says when a poll's PRs are stored, so the test reads the
// database only once the loop is done writing.
type signallingStorage struct {
	*storage.DbStorage
	stored chan struct{}
}

func (s *signallingStorage) StoreRepoPrs(prs []types.ViewPr) error {
	defer close(s.stored)
	return s.DbStorage.StoreRepoPrs(prs)
}
