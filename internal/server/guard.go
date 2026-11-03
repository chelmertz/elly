package server

import (
	"net"
	"net/http"
	"net/url"
	"strings"
)

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
//   - A write whose Origin is not one of those hosts is refused, and so is a
//     write whose Sec-Fetch-Site says another site (or another port, which is
//     "same-site") sent it. Clients that send neither (curl, scripts) are not
//     browsers and pass.
//   - No response may be framed: a site that frames a page could lay a decoy
//     over it and land the user's click on an Approve link.
func guard(listen string, allowedHosts []string, next http.Handler) http.Handler {
	hosts := newHostAllowlist(listen, allowedHosts)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
		w.Header().Set("X-Frame-Options", "DENY")
		if !hosts.allows(r.Host) {
			http.Error(w, "unexpected Host", http.StatusForbidden)
			return
		}
		if isWrite(r.Method) {
			if origin := r.Header.Get("Origin"); origin != "" {
				u, err := url.Parse(origin)
				if err != nil || !hosts.allows(u.Host) {
					http.Error(w, "cross-site write", http.StatusForbidden)
					return
				}
			}
			if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
				http.Error(w, "cross-site write", http.StatusForbidden)
				return
			}
		}
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

func isLoopback(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func isWrite(method string) bool {
	return method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions
}
