package server

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
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

// Every response carries the base headers, refusals and 404s included: a
// page that frames elly could lay a decoy over it and land the user's click
// on an Approve link, and a response sniffed as another type, or read by
// another origin as a script or image, leaks what it holds.
func TestGuardBaseHeadersOnEveryResponse(t *testing.T) {
	handler := guard("localhost:9876", nil, newRoutes(t, "").mux)
	for _, c := range []struct{ method, path, host string }{
		{"GET", "/", "localhost:9876"},
		{"GET", "/next", "localhost:9876"},
		{"GET", "/next", "attacker.example:9876"},
		{"GET", "/api/v0/prs", "localhost:9876"},
		{"GET", "/no-such-page", "localhost:9876"},
		{"PATCH", "/api/v0/prs", "localhost:9876"},
		{"POST", "/api/v0/prs/refresh", "localhost:9876"},
	} {
		r := httptest.NewRequest(c.method, c.path, nil)
		r.Host = c.host
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		want := map[string]string{
			"X-Frame-Options":              "DENY",
			"X-Content-Type-Options":       "nosniff",
			"Cross-Origin-Resource-Policy": "same-origin",
		}
		for h, v := range want {
			if got := w.Header().Get(h); got != v {
				t.Errorf("%s %s from %s (%d): %s %q, want %q", c.method, c.path, c.host, w.Code, h, got, v)
			}
		}
		csp := w.Header().Get("Content-Security-Policy")
		for _, directive := range []string{"default-src 'none'", "script-src 'sha256-", "style-src 'unsafe-inline'", "connect-src 'self'", "form-action 'self'", "base-uri 'none'", "frame-ancestors 'none'"} {
			if !strings.Contains(csp, directive) {
				t.Errorf("%s %s from %s (%d): Content-Security-Policy %q lacks %q", c.method, c.path, c.host, w.Code, csp, directive)
			}
		}
	}
}

// The page's one inline script runs only if its hash is the one in the
// header; html/template strips JS comments, so the hash has to be of the
// page as served, not of next.html.
func TestThePageScriptIsTheOneTheCSPAllows(t *testing.T) {
	handler := guard("localhost:9876", nil, newRoutes(t, "").mux)
	r := httptest.NewRequest("GET", "/", nil)
	r.Host = "localhost:9876"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	scripts := regexp.MustCompile(`(?s)<script>(.*?)</script>`).FindAllStringSubmatch(w.Body.String(), -1)
	if len(scripts) != 1 {
		t.Fatalf("the page has %d inline scripts, want 1", len(scripts))
	}
	sum := sha256.Sum256([]byte(scripts[0][1]))
	want := "script-src 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
	if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, want) {
		t.Errorf("Content-Security-Policy %q does not allow the page's script (%s)", csp, want)
	}
}

func TestInlineScriptRefusesATemplateAction(t *testing.T) {
	if _, err := inlineScript(`<p>{{.X}}</p><script>let a = 1;</script>`); err != nil {
		t.Errorf("an action outside the script: %v", err)
	}
	for _, page := range []string{
		`<script>let a = "{{.X}}";</script>`,
		`<p>no script</p>`,
		`<script>a</script><script>b</script>`,
	} {
		if _, err := inlineScript(page); err == nil {
			t.Errorf("%q: no error", page)
		}
	}
}

// A cross-site form can POST text/plain or a urlencoded body without a
// preflight, but not application/json, so a write with a body must be JSON
// unless it carries a bearer token, which a browser cannot add cross-site
// without a preflight either.
func TestGuardWritesWithABodyMustBeJSON(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	cases := []struct {
		name, method, body, contentType, auth string
		want                                  int
	}{
		{"json", "PUT", `{"token":"x"}`, "application/json", "", http.StatusNoContent},
		{"json with a charset", "PUT", `{}`, "application/json; charset=utf-8", "", http.StatusNoContent},
		{"a form's text/plain", "PUT", `{"token":"x"}`, "text/plain", "", http.StatusUnsupportedMediaType},
		{"a form's urlencoded body", "POST", `a=b`, "application/x-www-form-urlencoded", "", http.StatusUnsupportedMediaType},
		{"no content type", "PUT", `{}`, "", "", http.StatusUnsupportedMediaType},
		{"bearer token, no content type (claude-review before it sent one)", "PUT", `{}`, "", "Bearer t", http.StatusNoContent},
		{"no body (bury, poll now, clear)", "POST", "", "", "", http.StatusNoContent},
		{"a read is not a write", "GET", `x`, "text/plain", "", http.StatusNoContent},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(c.method, "/api/v0/config/pat", strings.NewReader(c.body))
			r.Host = "localhost:9876"
			if c.contentType != "" {
				r.Header.Set("Content-Type", c.contentType)
			}
			if c.auth != "" {
				r.Header.Set("Authorization", c.auth)
			}
			w := httptest.NewRecorder()
			guard("localhost:9876", nil, ok).ServeHTTP(w, r)
			if w.Code != c.want {
				t.Errorf("got %d, want %d", w.Code, c.want)
			}
		})
	}
	// A chunked body has no length up front and is still a body.
	r := httptest.NewRequest("PUT", "/api/v0/config/pat", strings.NewReader("{}"))
	r.Host = "localhost:9876"
	r.ContentLength = -1
	w := httptest.NewRecorder()
	guard("localhost:9876", nil, ok).ServeHTTP(w, r)
	if w.Code != http.StatusUnsupportedMediaType {
		t.Errorf("chunked body without a content type: %d", w.Code)
	}
}
