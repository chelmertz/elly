package server

import (
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chelmertz/elly/internal/backoff"
	"github.com/chelmertz/elly/internal/storage"
	"github.com/chelmertz/elly/internal/types"
)

func TestPageStateOf(t *testing.T) {
	polled := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name       string
		token      bool
		fetched    time.Time
		prs, onYou int
		want       pageState
	}{
		{"fresh install", false, time.Time{}, 0, 0, stateNoToken},
		{"no token wins over stale rows", false, polled, 3, 1, stateNoToken},
		{"token saved, poll pending", true, time.Time{}, 0, 0, stateFirstPoll},
		{"polled, nothing found", true, polled, 0, 0, stateNothing},
		{"polled, nothing on you", true, polled, 4, 0, stateClear},
		{"polled, something on you", true, polled, 4, 2, stateList},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := pageStateOf(c.token, c.fetched, c.prs, c.onYou); got != c.want {
				t.Errorf("got %s, want %s", got, c.want)
			}
		})
	}
}

// The walk a new user takes, through the real handler and store: each step
// is the event that moves the state, and each state says its own thing and
// none of the others'.
func TestPageStateWalk(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store := storage.NewStorage(logger, filepath.Join(t.TempDir(), "elly.db"))
	tracker := backoff.New(logger, time.Minute)
	t.Cleanup(tracker.Stop)
	w := routesWorld{mux: routes(HttpServerConfig{Store: store, Logger: logger, Tracker: tracker}), store: store, tracker: tracker}

	says := map[pageState]string{
		stateNoToken:   "Add a GitHub token",
		stateFirstPoll: "Reading your pull requests",
		stateNothing:   "No open pull requests involve you",
		stateClear:     "Nothing waits on you",
	}
	check := func(want pageState) {
		t.Helper()
		page := w.do("GET", "/", "").Body.String()
		if !strings.Contains(page, `<div class="rows" data-state="`+string(want)+`">`) {
			t.Fatalf("want state %s", want)
		}
		for st, text := range says {
			if has := strings.Contains(page, text); has != (st == want) {
				t.Errorf("state %s: %q shown=%v", want, text, has)
			}
		}
		if poll := strings.Contains(page, `class="poll"`); poll == (want == stateNoToken) {
			t.Errorf("state %s: poll now shown=%v", want, poll)
		}
		if connect := strings.Contains(page, "Connect GitHub"); connect != (want == stateNoToken) {
			t.Errorf("state %s: dialog titled Connect GitHub=%v", want, connect)
		}
	}
	now := time.Now()
	unrelated := types.ViewPr{Url: "https://github.com/o/r/pull/1", Title: "t", Author: "someone", LastUpdated: now, RawJsonResponse: []byte("{}")}
	asked := types.ViewPr{Url: "https://github.com/o/r/pull/2", Title: "t", Author: "someone", ReviewRequestedOfUser: true, LastUpdated: now, RawJsonResponse: []byte("{}")}

	check(stateNoToken)
	if err := store.StorePAT("ghp_x", "me", time.Time{}); err != nil {
		t.Fatal(err)
	}
	check(stateFirstPoll)
	steps := []struct {
		prs  []types.ViewPr
		want pageState
	}{
		{nil, stateNothing},
		{[]types.ViewPr{unrelated}, stateClear},
		{[]types.ViewPr{unrelated, asked}, stateList},
		{[]types.ViewPr{unrelated}, stateClear},
	}
	for _, s := range steps {
		if err := store.StoreRepoPrs(s.prs); err != nil {
			t.Fatal(err)
		}
		check(s.want)
	}
	if err := store.ClearPAT(); err != nil {
		t.Fatal(err)
	}
	check(stateNoToken)
}

// Counts before a poll read as "all clear" on a page that has read nothing.
func TestStatsWaitForAPoll(t *testing.T) {
	for st, hidden := range map[pageState]bool{stateNoToken: true, stateFirstPoll: true, stateNothing: false, stateClear: false, stateList: false} {
		page := renderNext(t, NextData{State: st})
		if got := strings.Contains(page, `<p class="stats" hidden>`); got != hidden {
			t.Errorf("%s: stats hidden=%v, want %v", st, got, hidden)
		}
	}
}
