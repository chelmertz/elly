package main

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chelmertz/elly/internal/backoff"
	"github.com/chelmertz/elly/internal/notify"
	"github.com/chelmertz/elly/internal/types"
)

// recordingStorage counts what a poll stores, and can refuse the PRs.
type recordingStorage struct {
	*testStorage
	refusePrs  bool
	storedPrs  atomic.Int32
	notifySeen atomic.Int32
}

func (s *recordingStorage) StoreRepoPrs([]types.ViewPr) error {
	s.storedPrs.Add(1)
	if s.refusePrs {
		return errors.New("disk full")
	}
	return nil
}

func (s *recordingStorage) StoreNotifySeen(map[string][]string) error {
	s.notifySeen.Add(1)
	return nil
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// A stored poll goes on to the notifications; a poll whose PRs could not be
// stored stops there, and only failures are logged as such.
func TestRefreshLoop_NotifiesOnlyAfterStoringThePrs(t *testing.T) {
	githubAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(graphqlSearchResponse()) //nolint:errcheck
	}))
	t.Cleanup(githubAPI.Close)
	for _, refuse := range []bool{false, true} {
		store := &recordingStorage{testStorage: newTestStorage("test-token", "testuser"), refusePrs: refuse}
		var logs syncBuffer
		logger := slog.New(slog.NewTextHandler(&logs, nil))
		tracker := backoff.New(logger, time.Hour)
		go startRefreshLoop(store, tracker, githubAPI.URL, func(notify.Notification) {}, logger)
		waitFor(t, "the poll to be stored", func() bool { return store.storedPrs.Load() == 1 })
		time.Sleep(50 * time.Millisecond) // the rest of the poll after storing
		tracker.Stop()
		l := logs.String()
		if got := store.notifySeen.Load() == 1; got == refuse {
			t.Errorf("refuse=%v: notifications ran = %v", refuse, got)
		}
		if strings.Contains(l, "could not store degradations") {
			t.Errorf("refuse=%v: stored degradations logged as a failure:\n%s", refuse, l)
		}
		if got := strings.Contains(l, "could not store prs"); got != refuse {
			t.Errorf("refuse=%v: logged a PR store failure = %v:\n%s", refuse, got, l)
		}
	}
}
