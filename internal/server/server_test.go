package server

import (
	"strings"
	"testing"
	"time"

	"github.com/chelmertz/elly/internal/types"
)

func TestPrsAtOrAboveSortsTheFilteredList(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	pr := func(url string, loc int, status string) types.ViewPr {
		return types.ViewPr{Url: url, Author: "someone", Additions: loc, ReviewStatus: status, LastUpdated: now}
	}
	// Scores: big 10, approved -100 (filtered out), small 50. Sorting the
	// filtered list by the unfiltered list's indexes compares big with the
	// dropped PR and leaves big first.
	prs := []types.ViewPr{
		pr("big", 400, ""),
		pr("approved", 10, "APPROVED"),
		pr("small", 10, ""),
	}

	got := prsAtOrAbove(prs, "me", 1, now)

	var urls []string
	for _, p := range got {
		urls = append(urls, p.Url)
	}
	if strings.Join(urls, ",") != "small,big" {
		t.Errorf("got %v, want [small big]", urls)
	}
}
