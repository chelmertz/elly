package server

import (
	"net"
	"net/http"
	"net/url"
)

// guard stops web pages from talking to elly. It listens on localhost, where
// a browser lets any site send it requests: a cross-site POST arrives without
// a CORS preflight, and DNS rebinding lets a site read responses by pointing
// its own name at 127.0.0.1.
//
//   - Host must be the listen address, or another loopback name on the same
//     port. A rebound request carries the attacker's name. A wildcard bind
//     (":9876", e.g. in Docker) has no single name, so it skips this check.
//   - A write whose Origin is another site is refused. Clients that send no
//     Origin (curl, scripts) are not browsers and pass.
func guard(listen string, next http.Handler) http.Handler {
	listenHost, listenPort, _ := net.SplitHostPort(listen)
	wildcard := listenHost == "" || net.ParseIP(listenHost).IsUnspecified()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !wildcard && !allowedHost(r.Host, listenHost, listenPort) {
			http.Error(w, "unexpected Host", http.StatusForbidden)
			return
		}
		if isWrite(r.Method) {
			if origin := r.Header.Get("Origin"); origin != "" {
				u, err := url.Parse(origin)
				if err != nil || u.Host != r.Host {
					http.Error(w, "cross-site write", http.StatusForbidden)
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

func allowedHost(host, listenHost, listenPort string) bool {
	h, p, err := net.SplitHostPort(host)
	if err != nil || p != listenPort {
		return false
	}
	return h == listenHost || (isLoopback(listenHost) && isLoopback(h))
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
