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

func TestPrParts(t *testing.T) {
	if o, r, n, ok := PrParts("https://github.com/o.x/r-y/pull/7"); !ok || o != "o.x" || r != "r-y" || n != "7" {
		t.Errorf("a PR URL: %q %q %q %v", o, r, n, ok)
	}
	for _, bad := range []string{
		"https://github.com/o/r/issues/7",
		"https://github.com/./r/pull/7",
		"https://github.com/o/../pull/7",
		"https://github.com/o/r/pull/07",
	} {
		if _, _, _, ok := PrParts(bad); ok {
			t.Errorf("%s taken apart", bad)
		}
	}
}

func TestLinkNamesPr(t *testing.T) {
	const pr = "https://github.com/Owner/repo/pull/7"
	for link, want := range map[string]bool{
		"https://github.com/owner/REPO/pull/7":         true,
		"HTTPS://github.com/Owner/repo/pull/7":         true,
		"https://github.com/Owner/repo/pull/7/files#x": true,
		"https://GitHub.com/Owner/repo/pull/7?w=1":     true,
		"https://github.com/Owner/repo/pull":           false,
		"https://github.com/Owner/repo/pull/8":         false,
		"https://github.com/Owner/repo/issues/7":       false,
		"https://github.com/Owner/other/pull/7":        false,
		"https://github.com/Other/repo/pull/7":         false,
		"https://evil.test/Owner/repo/pull/7":          false,
		"https://user@github.com/Owner/repo/pull/7":    false,
		"https://github.com/Owner/repo/pull/7/../../8": false,
		"https:Owner/repo/pull/7":                      false,
		"https://github.com/%zz":                       false,
		"claude-review://owner/Repo/7":                 true,
		"claude-review://Owner/repo/7/approve":         true,
		"claude-review://Owner/repo/7/approve/x":       false,
		"claude-review://Owner/repo":                   false,
		"claude-review://Owner/repo/8":                 false,
		"claude-review://Owner/other/7":                false,
		"claude-review://Other/repo/7":                 false,
		"claude-review://Owner/repo/7/approve?token=t": false,
		"claude-review://Owner/repo/7#x":               false,
		"prr://Owner/repo/7":                           false,
	} {
		if got := LinkNamesPr(pr, link); got != want {
			t.Errorf("LinkNamesPr(%s) = %v, want %v", link, got, want)
		}
	}
	if LinkNamesPr("https://github.com/Owner/repo/issues/7", pr) {
		t.Error("a link matched a URL that is not a PR")
	}
}
