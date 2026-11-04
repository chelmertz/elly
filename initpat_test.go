package main

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/chelmertz/elly/internal/storage"
)

type failingClearStorage struct{ *testStorage }

func (failingClearStorage) ClearPAT() error { return errors.New("disk full") }

// A PAT github rejects is cleared; only a clear that fails is worth a warning.
func TestInitPAT_WarnsOnlyWhenTheInvalidPATCannotBeCleared(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	for _, c := range []struct {
		name  string
		store storage.Storage
		warn  bool
	}{
		{"cleared", newTestStorage("t", "u"), false},
		{"clear fails", failingClearStorage{newTestStorage("t", "u")}, true},
	} {
		var logs bytes.Buffer
		setup, err := initPAT(c.store, srv.URL, slog.New(slog.NewTextHandler(&logs, nil)))
		if err != nil || !setup {
			t.Fatalf("%s: setup %v err %v, want setup mode", c.name, setup, err)
		}
		if got := strings.Contains(logs.String(), "could not clear invalid PAT"); got != c.warn {
			t.Errorf("%s: warned = %v, want %v\n%s", c.name, got, c.warn, logs.String())
		}
	}
}

// A valid GITHUB_PAT wins over the stored one, is stored in its place and
// leaves the environment.
func TestInitPAT_StoresAValidEnvToken(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			_, _ = w.Write(graphqlViewerResponse("envuser"))
			return
		}
		_, _ = w.Write(graphqlSearchResponse())
	}))
	t.Cleanup(srv.Close)
	t.Setenv("GITHUB_PAT", "from-env")
	store := newTestStorage("stored", "u")
	setup, err := initPAT(store, srv.URL, slog.New(slog.DiscardHandler))
	if err != nil || setup {
		t.Fatalf("setup %v err %v", setup, err)
	}
	if store.pat.Token != "from-env" {
		t.Errorf("stored token %q, want the env token", store.pat.Token)
	}
	if v, set := os.LookupEnv("GITHUB_PAT"); set {
		t.Errorf("GITHUB_PAT still set: %q", v)
	}
}
