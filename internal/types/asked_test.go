package types

import "testing"

// The resolved Asked values cross GET /api/v0/prs; claude-review's
// askedRequested and askedNotAsked name the same literals and pin them on
// its side.
func TestAskedLiterals(t *testing.T) {
	if AskedRequested != "requested" || AskedNotAsked != "not_asked" {
		t.Errorf("Asked literals moved: %q %q; move claude-review's with them", AskedRequested, AskedNotAsked)
	}
}
