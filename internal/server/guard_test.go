package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGuard(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })

	cases := []struct {
		name    string
		listen  string
		allowed []string
		method  string
		host    string
		origin  string
		fetch   string // Sec-Fetch-Site
		want    int
	}{
		{"page from the listen address", "localhost:9876", nil, "GET", "localhost:9876", "", "", http.StatusNoContent},
		{"loopback alias on the same port", "localhost:9876", nil, "GET", "127.0.0.1:9876", "", "", http.StatusNoContent},
		{"ipv6 loopback on the same port", "localhost:9876", nil, "GET", "[::1]:9876", "", "", http.StatusNoContent},
		{"dns rebinding: a foreign name", "localhost:9876", nil, "GET", "attacker.example:9876", "", "", http.StatusForbidden},
		{"loopback on another port", "localhost:9876", nil, "GET", "localhost:1234", "", "", http.StatusForbidden},
		{"wildcard bind still checks the host", ":9876", nil, "GET", "nas.lan:9876", "", "", http.StatusForbidden},
		{"wildcard bind, rebinding with a matching origin", "0.0.0.0:9876", nil, "POST", "attacker.example:9876", "http://attacker.example:9876", "", http.StatusForbidden},
		{"wildcard bind answers loopback (docker -p 127.0.0.1:9876:9876)", "0.0.0.0:9876", nil, "GET", "localhost:9876", "", "", http.StatusNoContent},
		{"an -allowed-hosts name (reverse proxy)", ":9876", []string{"elly.example"}, "GET", "elly.example", "", "", http.StatusNoContent},
		{"an -allowed-hosts name is matched case-insensitively", ":9876", []string{"Elly.Example"}, "GET", "elly.EXAMPLE", "", "", http.StatusNoContent},
		{"a write from the proxied page", ":9876", []string{"elly.example"}, "PUT", "elly.example", "https://elly.example", "same-origin", http.StatusNoContent},
		{"a named listen address", "nas.lan:9876", nil, "GET", "nas.lan:9876", "", "", http.StatusNoContent},
		{"write without origin (curl, scripts)", "localhost:9876", nil, "POST", "localhost:9876", "", "", http.StatusNoContent},
		{"write from elly's own page", "localhost:9876", nil, "POST", "localhost:9876", "http://localhost:9876", "same-origin", http.StatusNoContent},
		{"write from the page opened by another loopback name", "localhost:9876", nil, "POST", "localhost:9876", "http://127.0.0.1:9876", "", http.StatusNoContent},
		{"cross-site write", "localhost:9876", nil, "POST", "localhost:9876", "https://evil.example", "", http.StatusForbidden},
		{"write from another local port", "localhost:9876", nil, "POST", "localhost:9876", "http://localhost:1234", "", http.StatusForbidden},
		{"cross-site write on a wildcard bind", ":9876", nil, "PUT", "localhost:9876", "https://evil.example", "", http.StatusForbidden},
		{"opaque origin write", "localhost:9876", nil, "DELETE", "localhost:9876", "null", "", http.StatusForbidden},
		{"cross-site write without origin", "localhost:9876", nil, "POST", "localhost:9876", "", "cross-site", http.StatusForbidden},
		{"same-site write (another port) without origin", "localhost:9876", nil, "PUT", "localhost:9876", "", "same-site", http.StatusForbidden},
		{"a write the user typed (none)", "localhost:9876", nil, "POST", "localhost:9876", "", "none", http.StatusNoContent},
		{"cross-site read is left to CORS", "localhost:9876", nil, "GET", "localhost:9876", "https://evil.example", "cross-site", http.StatusNoContent},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(c.method, "/api/v0/prs", nil)
			r.Host = c.host
			if c.origin != "" {
				r.Header.Set("Origin", c.origin)
			}
			if c.fetch != "" {
				r.Header.Set("Sec-Fetch-Site", c.fetch)
			}
			w := httptest.NewRecorder()
			guard(c.listen, c.allowed, ok).ServeHTTP(w, r)
			if w.Code != c.want {
				t.Errorf("got %d, want %d", w.Code, c.want)
			}
		})
	}
}

// A page that frames elly could lay a decoy over it and land the user's
// click on an Approve link, so no response may be framed, refusals included.
func TestGuardForbidsFraming(t *testing.T) {
	handler := guard("localhost:9876", nil, newRoutes(t, "").mux)
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
