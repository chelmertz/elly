package server

import (
	"context"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
	handedOver := pr("8", "a")
	handedOver.ReviewRequestedFromUsers = []string{"me"}
	handedOver.Annotations = []types.Annotation{{Source: "claude-review", State: types.AnnotationRunning, Note: "started by tick"}}
	return []types.ViewPr{buried, iced, involved, waiting, red, review, merge, handedOver}
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
	data := buildNext(nextFixture(now), "me", now, nil, nil)
	for _, g := range data.Groups {
		if len(g.Rows) != 1 {
			t.Errorf("group %s has %d rows, want 1", g.Name, len(g.Rows))
		}
	}
	page := renderNext(t, data)
	for _, g := range points.Groups {
		heading := `<h2 class="group-head">` + g.Name()
		if g.Folded() {
			heading = `<span class="fold-name">` + g.Name() + `</span><span class="fold-count">1</span>`
		}
		if g != points.GroupOneClick && !strings.Contains(html.UnescapeString(page), heading) {
			t.Errorf("page lacks the heading %q", heading)
		}
		if !strings.Contains(page, `id="`+g.Slug()+`"`) {
			t.Errorf("page lacks the group anchor %q", g.Slug())
		}
	}
	for n := 1; n <= 8; n++ {
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
	page := renderNext(t, buildNext([]types.ViewPr{pr}, "me", now, nil, nil))
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
	page := renderNext(t, buildNext([]types.ViewPr{pr}, "me", now, []string{"claude-review"}, nil))
	if !strings.Contains(page, `href="claude-review://o/r/1/approve">Approve</a>`) {
		t.Error("the Approve verb does not follow the annotation link")
	}
	if !strings.Contains(page, `<span class="scheme">claude-review · approve</span>`) {
		t.Error("the link's verb is not shown beside its label")
	}
	page = renderNext(t, buildNext([]types.ViewPr{pr}, "me", now, nil, nil))
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
	for _, g := range buildNext(prs, "me", now, nil, nil).Groups {
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

// A note that needs the user is readable without opening the row, and a
// stuck PR stands out by its edge and its tag. The note is the session's own
// text, which may echo the PR, so it is escaped like any PR field.
func TestNextShowsWhatNeedsTheUserUnderTheRow(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	hostile := `the round has a question for you: <script>alert("x")</script> & "quotes"`
	row := func(n, state, note string) types.ViewPr {
		return types.ViewPr{Url: "https://github.com/o/r/pull/" + n, Author: "a", LastUpdated: now,
			Annotations: []types.Annotation{{Source: "claude-review", State: state, Note: note}}}
	}
	prs := []types.ViewPr{
		row("1", types.AnnotationNeedsYou, hostile),
		row("2", types.AnnotationStuck, "a post was refused over the limit"),
		row("3", types.AnnotationRunning, "started by a click"),
		row("4", types.AnnotationHandled, "round posted its review"),
	}
	page := renderNext(t, buildNext(prs, "me", now, nil, nil))
	if strings.Contains(page, "<script>alert") {
		t.Fatal("an annotation note reached the page unescaped")
	}
	for _, want := range []string{
		`<div class="ask">the round has a question for you: &lt;script&gt;alert(&#34;x&#34;)&lt;/script&gt; &amp; &#34;quotes&#34;</div>`,
		`<div class="ask">a post was refused over the limit</div>`,
		`<li class="pr stuck" id="pr-o-r-2"`,
		`<span class="tag stuck">claude ! stuck</span>`,
		`<div class="note">started by a click</div>`,
		`<div class="note">round posted its review</div>`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %s", want)
		}
	}
	if strings.Count(page, `class="pr stuck"`) != 1 {
		t.Error("only the stuck row has the stuck edge")
	}
}

func TestNextAttachVerbFollowsTheAnnotationLink(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	pr := types.ViewPr{Url: "https://github.com/o/r/pull/1", Author: "a", LastUpdated: now, ReviewRequestedFromUsers: []string{"me"},
		Annotations: []types.Annotation{{Source: "claude-review", State: types.AnnotationRunning,
			Links: []types.AnnotationLink{{Label: "Attach", Url: "claude-review://o/r/1/attach"}}}}}
	data := buildNext([]types.ViewPr{pr}, "me", now, []string{"claude-review"}, nil)
	if data.OnYou != 0 {
		t.Errorf("a handed-over PR counts as on the user: %d", data.OnYou)
	}
	if page := renderNext(t, data); !strings.Contains(page, `href="claude-review://o/r/1/attach">Attach</a>`) {
		t.Error("the Attach verb does not follow the annotation link")
	}
}

// An empty group takes no room in the list; the menu keeps its name with a
// dimmed zero that links nowhere.
func TestNextLeavesEmptyGroupsOut(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	prs := nextFixture(now)[1:] // without the buried PR
	page := renderNext(t, buildNext(prs, "me", now, nil, nil))
	if strings.Contains(page, `id="buried"`) || strings.Contains(page, `id="buried-list"`) {
		t.Error("the empty Buried group rendered in the list")
	}
	if !strings.Contains(page, `<span class="zero"><span>Buried</span><span>0</span></span>`) {
		t.Error("the menu lacks a dimmed Buried 0")
	}
	if strings.Contains(page, `href="#buried"`) {
		t.Error("the menu links to a group that is not on the page")
	}
}

// Each folded group is one full-width toggle, and the first of them opens
// the folded block apart from the rows on the user.
func TestNextFoldedGroupsAreOneToggleRowEach(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	page := renderNext(t, buildNext(nextFixture(now), "me", now, nil, nil))
	want := `<button type="button" class="fold" aria-expanded="false" aria-controls="with-claude-list"><span class="fold-name">With claude · handed over, nothing for you</span><span class="fold-count">1</span></button>`
	if !strings.Contains(page, want) {
		t.Errorf("page lacks %s", want)
	}
	if strings.Count(page, `class="group folded first-folded"`) != 1 || !strings.Contains(page, `<section class="group folded first-folded" id="with-claude"`) {
		t.Error("With claude does not open the folded block, or more than one group does")
	}
	if strings.Contains(page, ">show ") {
		t.Error("a folded heading still says show N")
	}
}

// The page follows the system theme: a light palette on :root, a dark one
// under prefers-color-scheme, each defining every colour the other does.
func TestNextHasALightAndADarkPalette(t *testing.T) {
	b, err := index.ReadFile("next.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(b)
	if !strings.Contains(page, `<meta name="color-scheme" content="light dark">`) {
		t.Error(`next.html does not declare color-scheme "light dark"`)
	}
	light, dark := colourTokens(page, ":root {"), colourTokens(page, "@media (prefers-color-scheme: dark) {")
	if len(light) == 0 || len(dark) == 0 {
		t.Fatalf("light tokens %v, dark tokens %v: want both palettes", light, dark)
	}
	for tok := range light {
		if !dark[tok] {
			t.Errorf("%s has a light colour and no dark one", tok)
		}
	}
	for tok := range dark {
		if !light[tok] {
			t.Errorf("%s has a dark colour and no light one", tok)
		}
	}
}

// colourTokens are the custom properties set to a hex colour in the block
// that starts at the first occurrence of opener.
func colourTokens(page, opener string) map[string]bool {
	i := strings.Index(page, opener)
	if i < 0 {
		return nil
	}
	block, depth := page[i:], 0
	for j, c := range block {
		if c == '{' {
			depth++
		} else if c == '}' {
			if depth--; depth == 0 {
				block = block[:j]
				break
			}
		}
	}
	toks := map[string]bool{}
	for _, m := range regexp.MustCompile(`(--[a-z0-9-]+):\s*#`).FindAllStringSubmatch(block, -1) {
		toks[m[1]] = true
	}
	return toks
}

// A review asked of the user, in a repo claude-review runs in, offers to hand
// it over: Start opens claude-review://owner/repo/number. Nowhere else does,
// since the link starts a round that posts as the user.
func TestNextStartHandsAnAskedReviewToClaudeReview(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	asked := func(url string) types.ViewPr {
		return types.ViewPr{Url: url, Author: "a", LastUpdated: now, ReviewRequestedFromUsers: []string{"me"}}
	}
	running := asked("https://github.com/o/r/pull/4")
	running.Annotations = []types.Annotation{{Source: "claude-review", State: types.AnnotationRunning}}
	stuck := asked("https://github.com/o/r/pull/5")
	stuck.Annotations = []types.Annotation{{Source: "claude-review", State: types.AnnotationStuck}}
	notAsked := asked("https://github.com/o/r/pull/6")
	notAsked.ReviewRequestedFromUsers = nil
	notAsked.ThreadsActionable = 1
	prs := []types.ViewPr{
		asked("https://github.com/o/r/pull/1"),
		asked("https://github.com/other/r/pull/2"),
		asked("https://github.com/O/Only/pull/3"),
		running, stuck, notAsked,
		asked("https://github.com/o/../pull/7"),
	}
	repos := []string{"o/*", "O/only"}
	page := renderNext(t, buildNext(prs, "me", now, []string{"claude-review"}, repos))
	for _, want := range []string{
		`href="claude-review://o/r/1" title="Start claude-review">Start</a>`,
		`href="claude-review://O/Only/3" title="Start claude-review">Start</a>`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %s", want)
		}
	}
	if n := strings.Count(page, ">Start</a>"); n != 2 {
		t.Errorf("%d Start verbs, want 2: not outside the repos, not running, stuck, unasked or a dot-dot repo", n)
	}
	if page := renderNext(t, buildNext(prs, "me", now, nil, repos)); strings.Contains(page, ">Start</a>") {
		t.Error("Start renders although the claude-review scheme is not allowed")
	}
}

// The keyboard is page script, so it runs in a real browser: testdata/keys.mjs
// drives headless chromium over the DevTools protocol. Skipped where node or
// chromium is missing.
func TestPageKeys(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node on PATH")
	}
	var browser string
	for _, name := range []string{"chromium", "chromium-browser", "google-chrome"} {
		if browser, err = exec.LookPath(name); err == nil {
			break
		}
	}
	if err != nil {
		t.Skip("no chromium on PATH")
	}
	now := time.Now()
	page := filepath.Join(t.TempDir(), "page.html")
	if err := os.WriteFile(page, []byte(renderNext(t, buildNext(nextFixture(now), "me", now, nil, nil))), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, node, "testdata/keys.mjs", browser, page).CombinedOutput()
	if err != nil {
		t.Errorf("%v\n%s", err, out)
	}
}
