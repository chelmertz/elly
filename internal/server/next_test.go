package server

import (
	"html"
	"strings"
	"testing"
	"time"

	"github.com/chelmertz/elly/internal/points"
	"github.com/chelmertz/elly/internal/types"
)

// One PR per group, so the page has to render every group and every row.
func nextFixture(now time.Time) []types.ViewPr {
	fresh := now.Add(-time.Hour)
	pr := func(n, author string) types.ViewPr {
		return types.ViewPr{Url: "https://github.com/o/r/pull/" + n, Title: "pr " + n, Author: author, RepoOwner: "o", RepoName: "r", LastUpdated: fresh, ReviewedBy: []string{"x"}}
	}
	merge := pr("1", "me")
	merge.ReviewStatus = "APPROVED"
	review := pr("2", "a")
	review.ReviewRequestedFromUsers = []string{"me"}
	red := pr("3", "me")
	red.ChecksState = "FAILURE"
	waiting := pr("4", "me")
	waiting.ReviewRequestedFromUsers = []string{"a"}
	involved := pr("5", "a")
	iced := pr("6", "a")
	iced.LastUpdated = now.Add(-30 * 24 * time.Hour)
	buried := pr("7", "a")
	buried.Buried = true
	return []types.ViewPr{buried, iced, involved, waiting, red, review, merge}
}

func renderNext(t *testing.T, data NextData) string {
	t.Helper()
	temp, err := parseNext()
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := temp.Execute(&out, data); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func TestNextRendersEachGroupAndRowAnchor(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	data := buildNext(nextFixture(now), "me", now, nil)
	for _, g := range data.Groups {
		if len(g.Rows) != 1 {
			t.Errorf("group %s has %d rows, want 1", g.Name, len(g.Rows))
		}
	}
	page := renderNext(t, data)
	for _, g := range points.Groups {
		if g != points.GroupOneClick && !strings.Contains(html.UnescapeString(page), `<h2 class="group-head">`+g.Name()) {
			t.Errorf("page lacks the heading %q", g.Name())
		}
		if !strings.Contains(page, `id="`+g.Slug()+`"`) {
			t.Errorf("page lacks the group anchor %q", g.Slug())
		}
	}
	for n := 1; n <= 7; n++ {
		id := types.Anchor("https://github.com/o/r/pull/" + string(rune('0'+n)))
		if !strings.Contains(page, `<li class="pr" id="`+id+`"`) {
			t.Errorf("page lacks the row anchor %q", id)
		}
	}
	// the first group has no heading
	if strings.Contains(page, `<h2 class="group-head">One click`) {
		t.Error("the first group rendered a heading")
	}
	if strings.Index(page, `id="one-click"`) > strings.Index(page, `id="waiting"`) {
		t.Error("groups are out of order")
	}
	if !strings.Contains(page, "3 on you") || !strings.Contains(page, "1 on others") {
		t.Errorf("stats line wrong: %s", page[strings.Index(page, `class="stats"`):][:120])
	}
}

func TestNextEscapesPrFields(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	payload := `<img src=x onerror=alert(1)>`
	pr := types.ViewPr{
		Url:         "https://github.com/o/r/pull/1",
		Title:       payload,
		Author:      payload,
		RepoOwner:   payload,
		RepoName:    payload,
		LastUpdated: now,
		Annotations: []types.Annotation{{Source: "claude-review", State: types.AnnotationNeedsYou, Note: payload,
			Links: []types.AnnotationLink{{Label: payload, Url: "javascript:alert(1)"}}}},
	}
	page := renderNext(t, buildNext([]types.ViewPr{pr}, "me", now, nil))
	if strings.Contains(page, payload) {
		t.Error("next.html renders a PR field unescaped")
	}
	if !strings.Contains(page, `<span class="title">&lt;img src=x onerror=alert(1)&gt;</span>`) {
		t.Error("escaped PR title missing from the page")
	}
	if strings.Contains(page, "javascript:") {
		t.Error("an annotation link outside the allowlist reached the page")
	}
}

func TestNextVerbFollowsAnAllowedAnnotationLink(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	pr := types.ViewPr{Url: "https://github.com/o/r/pull/1", Author: "a", LastUpdated: now,
		Annotations: []types.Annotation{{Source: "claude-review", State: types.AnnotationNeedsYou,
			Links: []types.AnnotationLink{{Label: "Approve", Url: "claude-review://o/r/1/approve"}}}}}
	page := renderNext(t, buildNext([]types.ViewPr{pr}, "me", now, []string{"claude-review"}))
	if !strings.Contains(page, `href="claude-review://o/r/1/approve">Approve</a>`) {
		t.Error("the Approve verb does not follow the annotation link")
	}
	page = renderNext(t, buildNext([]types.ViewPr{pr}, "me", now, nil))
	if strings.Contains(page, "claude-review://") {
		t.Error("a link whose scheme is no longer allowed still renders")
	}
}

func TestReasonsByWeight(t *testing.T) {
	got := reasonsByWeight([]string{"+10: small", "-100: approved", "+80: asked"})
	if strings.Join(got, "|") != "approved|asked|small" {
		t.Errorf("got %v", got)
	}
}

func TestRepoNameGetsItsOwnerOnlyWhenAmbiguous(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	prs := []types.ViewPr{
		{Url: "https://github.com/a/api/pull/1", RepoOwner: "a", RepoName: "api", LastUpdated: now},
		{Url: "https://github.com/b/api/pull/2", RepoOwner: "b", RepoName: "api", LastUpdated: now},
		{Url: "https://github.com/a/web/pull/3", RepoOwner: "a", RepoName: "web", LastUpdated: now},
	}
	repos := map[string]bool{}
	for _, g := range buildNext(prs, "me", now, nil).Groups {
		for _, r := range g.Rows {
			repos[r.Repo] = true
		}
	}
	if !repos["a/api"] || !repos["b/api"] || !repos["web"] {
		t.Errorf("got %v", repos)
	}
}

func TestWaitCountsFromTheLastActivityBySomeoneElse(t *testing.T) {
	updated := time.Date(2026, 11, 3, 11, 0, 0, 0, time.UTC)
	pr := types.ViewPr{LastUpdated: updated, RawJsonResponse: []byte(`{
		"reviews": {"edges": [{"node": {"author": {"login": "a"}, "state": "APPROVED", "submittedAt": "2026-10-30T10:00:00Z"}}]},
		"comments": {"edges": [{"node": {"author": {"login": "me"}, "updatedAt": "2026-11-02T10:00:00Z"}}]}
	}`)}
	events := prEvents(pr)
	if len(events) != 2 || events[0].What != "a approved" {
		t.Fatalf("events: %+v", events)
	}
	if got := waitSince(pr, events, "me"); !got.Equal(time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("waitSince = %v, want the review by a, not the user's own comment", got)
	}
	if got := waitSince(types.ViewPr{LastUpdated: updated}, nil, "me"); !got.Equal(updated) {
		t.Errorf("without events: %v, want LastUpdated", got)
	}
}

func TestNextRoute(t *testing.T) {
	w := newRoutes(t, "")
	rec := w.do("GET", "/next", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `id="pr-o-r-1"`) {
		t.Errorf("GET /next: %d", rec.Code)
	}
	if rec := w.do("GET", "/", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), "small one") {
		t.Errorf("the classic page broke: %d", rec.Code)
	}
}
