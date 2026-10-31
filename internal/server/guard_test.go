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
