package main

import (
	"runtime/debug"
	"testing"
)

// The page names the build's commit, so a screenshot says which build it is.
func TestBuildVersion(t *testing.T) {
	info := func(version, rev string) func() (*debug.BuildInfo, bool) {
		return func() (*debug.BuildInfo, bool) {
			bi := &debug.BuildInfo{Main: debug.Module{Version: version}}
			if rev != "" {
				bi.Settings = []debug.BuildSetting{{Key: "vcs.revision", Value: rev}}
			}
			return bi, true
		}
	}
	for _, c := range []struct {
		name, rev, want string
		read            func() (*debug.BuildInfo, bool)
	}{
		{"nix build", "373b0ab", "373b0ab", info("(devel)", "")},
		{"go build in a checkout", "", "373b0ab", info("(devel)", "373b0abd61be44eb19a93fc4bd5d5a1df8e0ddb8")},
		{"no commit known", "", "(devel)", info("(devel)", "")},
		{"no build info", "", "unknown", func() (*debug.BuildInfo, bool) { return nil, false }},
	} {
		if got := buildVersion(c.rev, c.read); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}
