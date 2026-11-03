package server

import (
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// maxBody caps every request body; routes that take one cap theirs lower.
const maxBody = 1 << 20

// guard stops web pages from talking to elly. It listens on localhost, where
// a browser lets any site send it requests: a cross-site POST arrives without
// a CORS preflight, and DNS rebinding lets a site read responses by pointing
// its own name at 127.0.0.1.
//
//   - Host must be a loopback name on the listen port, the listen address
//     itself, or one of allowedHosts (-allowed-hosts, for a reverse proxy).
//     A rebound request carries the attacker's name. A wildcard bind (":9876",
//     e.g. in Docker) gets the same check: it accepts loopback names, so a
//     port published on 127.0.0.1 works and nothing else does.
//   - A write whose Origin is not http on one of those hosts (or https on an
//     allowedHosts name) is refused, and so is a write whose Sec-Fetch-Site
//     says another site (or another port, which is "same-site") sent it.
//     Clients that send neither (curl, scripts) are not browsers and pass.
//   - A GET or HEAD whose Sec-Fetch-Site says another site sent it is refused
//     unless its Sec-Fetch-Mode is navigate: a link the user clicks
//     navigates, a script, an image or a style sheet reaching in does not.
//     A typed URL or a bookmark says "none", and curl sends neither.
//   - A write with a body must be application/json, which a cross-site form
//     cannot send without a preflight elly never answers. A request with a
//     bearer token is exempt: a browser cannot add one cross-site without a
//     preflight either.
//   - No body is read past maxBody.
//   - Every response elly's handlers write, refusals and 404s included,
//     carries the base headers:
//     no framing (a site that frames a page could lay a decoy over it and
//     land the user's click on an Approve link), nosniff, a same-origin
//     resource policy, and a CSP that runs only the page's own script.
func guard(listen string, allowedHosts []string, next http.Handler) http.Handler {
	hosts := newHostAllowlist(listen, allowedHosts)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", contentSecurityPolicy)
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		if !hosts.allows(r.Host) {
			http.Error(w, "unexpected Host", http.StatusForbidden)
			return
		}
		if !isWrite(r.Method) && r.Method != http.MethodOptions {
			if site := r.Header.Get("Sec-Fetch-Site"); (site == "cross-site" || site == "same-site") && r.Header.Get("Sec-Fetch-Mode") != "navigate" {
				http.Error(w, "cross-site read", http.StatusForbidden)
				return
			}
		}
		if isWrite(r.Method) {
			if origin := r.Header.Get("Origin"); origin != "" {
				u, err := url.Parse(origin)
				if err != nil || !hosts.allowsOrigin(u) {
					http.Error(w, "cross-site write", http.StatusForbidden)
					return
				}
			}
			if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
				http.Error(w, "cross-site write", http.StatusForbidden)
				return
			}
			if r.ContentLength != 0 && !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") && !isJSON(r.Header.Get("Content-Type")) {
				http.Error(w, "a write with a body must be application/json", http.StatusUnsupportedMediaType)
				return
			}
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		next.ServeHTTP(w, r)
	})
}

// hostAllowlist is the Host values elly answers, and the Origin hosts it
// takes writes from.
type hostAllowlist struct {
	listenHost string // empty for a wildcard bind
	listenPort string
	extra      map[string]bool
}

func newHostAllowlist(listen string, allowedHosts []string) hostAllowlist {
	listenHost, listenPort, _ := net.SplitHostPort(listen)
	if listenHost != "" && net.ParseIP(listenHost).IsUnspecified() {
		listenHost = ""
	}
	extra := map[string]bool{}
	for _, h := range allowedHosts {
		extra[strings.ToLower(h)] = true
	}
	return hostAllowlist{listenHost: strings.ToLower(listenHost), listenPort: listenPort, extra: extra}
}

// allows takes a Host header value, or an Origin's host: name and port, the
// port left out when it is the scheme's default.
func (a hostAllowlist) allows(hostport string) bool {
	hostport = strings.ToLower(hostport)
	if a.extra[hostport] {
		return true
	}
	h, p, err := net.SplitHostPort(hostport)
	if err != nil || p != a.listenPort {
		return false
	}
	return isLoopback(h) || (a.listenHost != "" && h == a.listenHost)
}

// allowsOrigin is allows for an Origin, which also names a scheme: elly
// serves http, so its own page's origin is http. An https origin passes only
// for an -allowed-hosts name, where a reverse proxy may terminate TLS.
func (a hostAllowlist) allowsOrigin(u *url.URL) bool {
	switch u.Scheme {
	case "http":
		return a.allows(u.Host)
	case "https":
		return a.extra[strings.ToLower(u.Host)]
	}
	return false
}

func isLoopback(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func isJSON(contentType string) bool {
	mt, _, err := mime.ParseMediaType(contentType)
	return err == nil && mt == "application/json"
}

func isWrite(method string) bool {
	return method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions
}
