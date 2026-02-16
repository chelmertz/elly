package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/chelmertz/elly/internal/github"
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
	pat     storage.StoredPAT
	hasPAT  bool
	cleared bool
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

func (s *testStorage) Prs() storage.StoredState                      { return storage.StoredState{} }
func (s *testStorage) StoreRepoPrs([]types.ViewPr) error             { return nil }
func (s *testStorage) Bury(string) error                             { return nil }
func (s *testStorage) Unbury(string) error                           { return nil }
func (s *testStorage) GetPr(string) (storage.Pr, error)              { return storage.Pr{}, nil }
func (s *testStorage) SetRateLimitUntil(time.Time) error             { return nil }
func (s *testStorage) IsRateLimitActive(time.Time) bool              { return false }
func (s *testStorage) GetRateLimitUntil() time.Time                  { return time.Time{} }

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
			name:          "401 unauthorized",
			handler:       fixedStatusHandler(401, `{"message":"Bad credentials"}`),
			wantSetupMode: true,
		},
		{
			name:          "403 forbidden",
			handler:       fixedStatusHandler(403, `{"message":"Forbidden"}`),
			wantSetupMode: true,
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
