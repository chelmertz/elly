package types

import (
	"strings"
	"testing"
)

// The kind is matched as a literal on purpose: the pr-status skill reads
// "checks_unreadable" from the API, so a rename here must fail a test.
func TestTokenLimitation(t *testing.T) {
	if !(Degradation{Kind: "checks_unreadable"}).TokenLimitation() {
		t.Error("checks_unreadable follows from the token type and must count as a token limitation")
	}
	if (Degradation{Kind: "review_requests_unreadable"}).TokenLimitation() {
		t.Error("review_requests_unreadable is a fault, not a token limitation")
	}
}

func TestAnchor(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/Owner/repo.name/pull/7": "pr-owner-repo_2ename-7",
		"https://github.com/o/r/pull/11534":         "pr-o-r-11534",
		`https://github.com/o/"><x/pull/1`:          "pr-o-_22_3e_3cx-1",
	} {
		if got := Anchor(in); got != want {
			t.Errorf("Anchor(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNumber(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/o/r/pull/11534": "11534",
		"https://github.com/o/r/pull/":      "",
		"https://github.com/o/r/pull/x1":    "",
	} {
		if got := Number(in); got != want {
			t.Errorf("Number(%q) = %q, want %q", in, got, want)
		}
	}
}

// The ends of each kept range survive as themselves.
func TestAnchorKeepsRangeEnds(t *testing.T) {
	if got := Anchor("https://github.com/az09-/r/pull/1"); got != "pr-az09_2d-r-1" {
		t.Errorf("got %q", got)
	}
}

// Distinct repos get distinct ids: GitHub names keep ".", "_" and "-" apart,
// and so must the anchor, or a link to one row lands on another.
func TestAnchorKeepsDistinctReposApart(t *testing.T) {
	seen := map[string]string{}
	for _, u := range []string{
		"https://github.com/acme/wid.get/pull/12",
		"https://github.com/acme/wid-get/pull/12",
		"https://github.com/acme/wid_get/pull/12",
		"https://github.com/acme/widget/pull/12",
		"https://github.com/a-b/c/pull/1",
		"https://github.com/a/b-c/pull/1",
		"https://github.com/a/b/pull/1",
		"https://github.com/a/b-1/pull/1",
	} {
		a := Anchor(u)
		if other, ok := seen[a]; ok {
			t.Errorf("%s and %s share the anchor %s", u, other, a)
		}
		seen[a] = u
		if strings.Trim(a, "abcdefghijklmnopqrstuvwxyz0123456789-_") != "" {
			t.Errorf("%s: anchor %q has a character outside [a-z0-9_-]", u, a)
		}
	}
}
