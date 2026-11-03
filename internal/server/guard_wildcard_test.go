package server

import "testing"

// A bind on an unspecified address is a wildcard bind, the same as one with
// no host at all.
func TestUnspecifiedBindIsAWildcard(t *testing.T) {
	for _, listen := range []string{"0.0.0.0:9876", "[::]:9876"} {
		if got := newHostAllowlist(listen, nil); got.listenHost != "" || got.listenPort != "9876" {
			t.Errorf("%s: listenHost %q port %q, want a wildcard on 9876", listen, got.listenHost, got.listenPort)
		}
	}
	if got := newHostAllowlist("127.0.0.1:9876", nil).listenHost; got != "127.0.0.1" {
		t.Errorf("a loopback bind became %q", got)
	}
}
