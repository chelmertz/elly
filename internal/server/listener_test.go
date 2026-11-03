package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A client that sends headers or a body slowly, or never reads the answer,
// must not hold a connection forever.
func TestTheServerHasTimeouts(t *testing.T) {
	s := newHTTPServer("localhost:0", http.NotFoundHandler(), defaultTimeouts)
	for name, d := range map[string]time.Duration{
		"ReadHeaderTimeout": s.ReadHeaderTimeout,
		"ReadTimeout":       s.ReadTimeout,
		"WriteTimeout":      s.WriteTimeout,
		"IdleTimeout":       s.IdleTimeout,
	} {
		if d <= 0 || d > 5*time.Minute {
			t.Errorf("%s is %v", name, d)
		}
	}
	if s.Addr != "localhost:0" {
		t.Errorf("Addr %q", s.Addr)
	}
}

// guard caps every body, so no handler can be made to read without end;
// annotations and the token cap theirs lower.
func TestGuardLimitsEveryBody(t *testing.T) {
	read := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err != nil {
			http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	for _, c := range []struct {
		size int
		want int
	}{{maxBody, http.StatusNoContent}, {maxBody + 1, http.StatusRequestEntityTooLarge}} {
		r := httptest.NewRequest("PUT", "/x", strings.NewReader(strings.Repeat("a", c.size)))
		r.Host = "localhost:9876"
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		guard("localhost:9876", nil, read).ServeHTTP(w, r)
		if w.Code != c.want {
			t.Errorf("%d bytes: %d, want %d", c.size, w.Code, c.want)
		}
	}
}

// Checking a pasted token can take longer than the server's write timeout,
// so its route extends its own deadline instead of the user seeing a reset.
func TestPatPutOutlivesTheWriteTimeout(t *testing.T) {
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer gh.Close()
	w := newRoutes(t, gh.URL)
	srv := httptest.NewUnstartedServer(nil)
	srv.Config = newHTTPServer("", guard(srv.Listener.Addr().String(), nil, w.mux), serverTimeouts{header: time.Second, read: time.Second, write: 100 * time.Millisecond, idle: time.Second})
	srv.Start()
	defer srv.Close()
	req, _ := http.NewRequest("PUT", srv.URL+"/api/v0/config/pat", strings.NewReader(`{"token":"ghp_x"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("the PUT was cut off: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck // test
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("got %d, want 400", resp.StatusCode)
	}
}
