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
