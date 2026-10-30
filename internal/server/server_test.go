package server

import (
	"strings"
	"testing"

	"github.com/chelmertz/elly/internal/points"
	"github.com/chelmertz/elly/internal/types"
)

func TestIndexEscapesPrFields(t *testing.T) {
	temp, err := parseIndex()
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
