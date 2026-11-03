package types

import "testing"

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
		"https://github.com/Owner/repo.name/pull/7": "pr-owner-repo-name-7",
		"https://github.com/o/r/pull/11534":         "pr-o-r-11534",
		`https://github.com/o/"><x/pull/1`:          "pr-o----x-1",
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
