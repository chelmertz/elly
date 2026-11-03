package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGuard(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })

	cases := []struct {
		name   string
		listen string
		method string
		host   string
		origin string
		want   int
	}{
		{"page from the listen address", "localhost:9876", "GET", "localhost:9876", "", http.StatusNoContent},
		{"loopback alias on the same port", "localhost:9876", "GET", "127.0.0.1:9876", "", http.StatusNoContent},
		{"ipv6 loopback on the same port", "localhost:9876", "GET", "[::1]:9876", "", http.StatusNoContent},
		{"dns rebinding: a foreign name", "localhost:9876", "GET", "attacker.example:9876", "", http.StatusForbidden},
		{"loopback on another port", "localhost:9876", "GET", "localhost:1234", "", http.StatusForbidden},
		{"wildcard bind checks no host", ":9876", "GET", "nas.lan:9876", "", http.StatusNoContent},
		{"write without origin (curl, scripts)", "localhost:9876", "POST", "localhost:9876", "", http.StatusNoContent},
		{"write from elly's own page", "localhost:9876", "POST", "localhost:9876", "http://localhost:9876", http.StatusNoContent},
		{"cross-site write", "localhost:9876", "POST", "localhost:9876", "https://evil.example", http.StatusForbidden},
		{"cross-site write on a wildcard bind", ":9876", "PUT", "nas.lan:9876", "https://evil.example", http.StatusForbidden},
		{"opaque origin write", "localhost:9876", "DELETE", "localhost:9876", "null", http.StatusForbidden},
		{"cross-site read is left to CORS", "localhost:9876", "GET", "localhost:9876", "https://evil.example", http.StatusNoContent},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(c.method, "/api/v0/prs", nil)
			r.Host = c.host
			if c.origin != "" {
				r.Header.Set("Origin", c.origin)
			}
			w := httptest.NewRecorder()
			guard(c.listen, ok).ServeHTTP(w, r)
			if w.Code != c.want {
				t.Errorf("got %d, want %d", w.Code, c.want)
			}
		})
	}
}

// A page that frames elly could lay a decoy over it and land the user's
// click on an Approve link, so no response may be framed, refusals included.
func TestGuardForbidsFraming(t *testing.T) {
	handler := guard("localhost:9876", newRoutes(t, "").mux)
	for _, c := range []struct{ path, host string }{
		{"/", "localhost:9876"},
		{"/next", "localhost:9876"},
		{"/next", "attacker.example:9876"},
	} {
		r := httptest.NewRequest("GET", c.path, nil)
		r.Host = c.host
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if got := w.Header().Get("Content-Security-Policy"); got != "frame-ancestors 'none'" {
			t.Errorf("GET %s from %s: Content-Security-Policy %q", c.path, c.host, got)
		}
		if got := w.Header().Get("X-Frame-Options"); got != "DENY" {
			t.Errorf("GET %s from %s: X-Frame-Options %q", c.path, c.host, got)
		}
	}
}
