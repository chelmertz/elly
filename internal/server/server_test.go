package server

import (
	"strings"
	"testing"
	"time"

	"github.com/chelmertz/elly/internal/points"
	"github.com/chelmertz/elly/internal/types"
)

func TestIndexEscapesPrFields(t *testing.T) {
	temp, err := parseIndex(nil)
	if err != nil {
		t.Fatal(err)
	}
	const url = "https://github.com/o/r/pull/1"
	payload := `<img src=x onerror=alert(1)>`
	data := IndexHtmlData{
		Prs: []types.ViewPr{{
			Url:       url,
			Title:     payload,
			Author:    payload,
			RepoOwner: payload,
			RepoName:  payload,
		}},
		PointsPerPrUrl: map[string]*points.Points{url: {Total: 1, Reasons: []string{payload}}},
	}
	var out strings.Builder
	if err := temp.Execute(&out, data); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), payload) {
		t.Error("index.html renders a PR field unescaped")
	}
	if !strings.Contains(out.String(), "&lt;img src=x onerror=alert(1)&gt;") {
		t.Error("escaped PR title missing from the page")
	}
}

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
