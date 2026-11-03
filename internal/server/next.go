package server

import (
	"encoding/json"
	"html/template"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/chelmertz/elly/internal/points"
	"github.com/chelmertz/elly/internal/storage"
	"github.com/chelmertz/elly/internal/types"
)

// NextData is what next.html renders: the PRs grouped by whose turn it is.
type NextData struct {
	Groups        []nextGroup
	LastRefreshed string
	// RefreshIntervalMinutes drives the page's own reload.
	RefreshIntervalMinutes int
	RateLimitedUntil       string
	Degradations           []types.Degradation
	SetupMode              bool
	OnYou                  int
	OnOthers               int
	// OldestOnYou is the longest wait among the PRs on the user, as a
	// bucket-free duration like "6 d", or "" when nothing is on the user.
	OldestOnYou string
	Version     string
	// Token is what the page says about the stored PAT, never the PAT
	// itself; nil in setup mode.
	Token *nextToken
}

type nextToken struct {
	Username string
	// Expires is "" for a token that never expires.
	Expires     string
	ExpiresSoon bool
}

// tokenExpiryWarning is how close to expiry the page starts warning.
const tokenExpiryWarning = 5 * 24 * time.Hour

func tokenInfo(pat storage.StoredPAT, now time.Time) *nextToken {
	t := &nextToken{Username: pat.Username}
	if pat.ExpiresAt.IsZero() {
		return t
	}
	left := pat.ExpiresAt.Sub(now)
	switch {
	case left <= 0:
		t.Expires, t.ExpiresSoon = "expired", true
	case left <= tokenExpiryWarning:
		// rounded up, so a token with 2 d 23 h left reads as 3 d
		t.Expires, t.ExpiresSoon = "expires in "+duration(left.Truncate(time.Hour)+time.Hour), true
	default:
		t.Expires = "expires " + pat.ExpiresAt.Format("Jan 2")
	}
	return t
}

type nextGroup struct {
	Name string
	// NavName is the name without its explanation, for the narrow menu.
	NavName string
	Slug    string
	Folded  bool
	// Heading is false for the first group, which the page shows without one.
	Heading bool
	// FirstFolded opens the block of folded groups, set apart from the rows
	// on the user.
	FirstFolded bool
	Rows        []nextRow
}

type nextRow struct {
	Anchor string
	Number string
	Title  string
	Url    string
	// Repo is the bare repository name, or owner/name when two listed PRs
	// come from repositories with the same name.
	Repo   string
	Author string
	// Search is what the filter matches against, lower case.
	Search string
	OnYou  bool
	// TurnAt is when the wait is counted from, RFC 3339; see waitSince.
	TurnAt      string
	Bucket      string
	Verb        string
	VerbHref    template.URL
	VerbPrimary bool
	Tag         *nextTag
	// Stuck marks the row's edge: nothing but the user moves the PR.
	Stuck bool
	// Ask is the note of an annotation that needs the user, shown under the
	// row; Note is claude-review's note on a PR handed over to it.
	Ask     string
	Note    string
	Why     []string
	History []nextEvent
	State   []string
	Links   []nextLink
	BuryUrl string
	Buried  bool
}

type nextTag struct {
	Symbol string
	Word   string
	Class  string
}

type nextEvent struct {
	At   string
	What string
}

type nextLink struct {
	Label string
	Href  template.URL
	Verb  string
}

// buildNext groups and orders the PRs for the page. Inside a group the
// order is the points order.
func buildNext(prs []types.ViewPr, username string, now time.Time, schemes []string) NextData {
	href := annotationHref(schemes)
	ranks := points.Rank(prs, username, now)
	sorted := slices.Clone(prs)
	slices.SortStableFunc(sorted, func(a, b types.ViewPr) int { return ranks[a.Url] - ranks[b.Url] })

	owners := map[string]map[string]bool{}
	for _, pr := range prs {
		if owners[pr.RepoName] == nil {
			owners[pr.RepoName] = map[string]bool{}
		}
		owners[pr.RepoName][pr.RepoOwner] = true
	}

	var data NextData
	byGroup := map[points.Group][]nextRow{}
	var oldest time.Duration
	for _, pr := range sorted {
		turn := points.TurnOf(pr, username, now)
		pts := points.StandardPrPoints(pr, username, now)
		events := prEvents(pr)
		since := waitSince(pr, events, username)

		repo := pr.RepoName
		if len(owners[pr.RepoName]) > 1 {
			repo = pr.RepoOwner + "/" + pr.RepoName
		}
		verbHref := href(pr.Url)
		if turn.VerbUrl != "" {
			verbHref = href(turn.VerbUrl)
		}
		row := nextRow{
			Anchor:      types.Anchor(pr.Url),
			Number:      types.Number(pr.Url),
			Title:       pr.Title,
			Url:         pr.Url,
			Repo:        repo,
			Author:      pr.Author,
			Search:      strings.ToLower(types.Number(pr.Url) + " " + pr.Title + " " + repo + " " + pr.Author),
			OnYou:       turn.Group.OnYou(),
			TurnAt:      since.Format(time.RFC3339),
			Bucket:      bucket(now.Sub(since)),
			Verb:        turn.Verb,
			VerbHref:    verbHref,
			VerbPrimary: turn.Group == points.GroupOneClick,
			Tag:         reviewTag(pr),
			Why:         append([]string{turn.Reason}, reasonsByWeight(pts.Reasons)...),
			History:     history(events),
			State:       stateFacts(pr, username),
			BuryUrl:     pr.ToggleBuryUrl(),
			Buried:      pr.Buried,
		}
		for _, a := range pr.Annotations {
			switch {
			case a.State == types.AnnotationNeedsYou || a.State == types.AnnotationStuck:
				row.Stuck = row.Stuck || a.State == types.AnnotationStuck
				if row.Ask == "" {
					row.Ask = a.Note
				}
			case a.Source == points.ClaudeReviewSource && row.Note == "":
				row.Note = a.Note
			}
			for _, l := range a.Links {
				row.Links = append(row.Links, nextLink{Label: l.Label, Href: href(l.Url), Verb: annotationVerb(l.Url)})
			}
		}
		if row.OnYou {
			data.OnYou++
			if w := now.Sub(since); w > oldest {
				oldest = w
			}
		} else if turn.Group == points.GroupOnOthers {
			data.OnOthers++
		}
		byGroup[turn.Group] = append(byGroup[turn.Group], row)
	}
	folded := false
	for i, g := range points.Groups {
		ng := nextGroup{Name: g.Name(), NavName: strings.SplitN(g.Name(), " · ", 2)[0], Slug: g.Slug(), Folded: g.Folded(), Heading: i > 0, Rows: byGroup[g]}
		if ng.Folded && len(ng.Rows) > 0 && !folded {
			ng.FirstFolded, folded = true, true
		}
		data.Groups = append(data.Groups, ng)
	}
	if data.OnYou > 0 {
		data.OldestOnYou = duration(oldest)
	}
	return data
}

// prEvent is one thing that happened on a PR, read from the search result
// elly stores per PR. It is what github returned on the last poll and no more:
// the last five PR comments, the last comment of each review thread on the
// first page, the first twenty reviews, and the head commit's author date.
type prEvent struct {
	At    time.Time
	Login string
	What  string
}

func prEvents(pr types.ViewPr) []prEvent {
	var raw struct {
		Comments struct {
			Edges []struct {
				Node struct {
					UpdatedAt string
					Author    struct{ Login string }
				}
			}
		}
		Reviews struct {
			Edges []struct {
				Node struct {
					Author      struct{ Login string }
					State       string
					SubmittedAt string
				}
			}
		}
		ReviewThreads struct {
			Edges []struct {
				Node struct {
					LastComment struct {
						Nodes []struct {
							Author    struct{ Login string }
							CreatedAt string
						}
					}
				}
			}
		}
		Commits struct {
			Nodes []struct {
				Commit struct {
					Author struct{ Date string }
				}
			}
		}
	}
	if len(pr.RawJsonResponse) == 0 || json.Unmarshal(pr.RawJsonResponse, &raw) != nil {
		return nil
	}
	var out []prEvent
	add := func(at, login, what string) {
		t, err := time.Parse(time.RFC3339, at)
		if err == nil {
			out = append(out, prEvent{At: t, Login: login, What: what})
		}
	}
	for _, e := range raw.Reviews.Edges {
		n := e.Node
		add(n.SubmittedAt, n.Author.Login, n.Author.Login+" "+reviewWords(n.State))
	}
	for _, e := range raw.Comments.Edges {
		add(e.Node.UpdatedAt, e.Node.Author.Login, e.Node.Author.Login+" commented")
	}
	for _, e := range raw.ReviewThreads.Edges {
		for _, c := range e.Node.LastComment.Nodes {
			add(c.CreatedAt, c.Author.Login, c.Author.Login+" wrote in a thread")
		}
	}
	for _, c := range raw.Commits.Nodes {
		// the author date, not the push: github does not return when a
		// commit arrived, so this can predate the push by days
		add(c.Commit.Author.Date, "", "latest commit authored")
	}
	slices.SortStableFunc(out, func(a, b prEvent) int { return a.At.Compare(b.At) })
	return out
}

func reviewWords(state string) string {
	switch state {
	case "APPROVED":
		return "approved"
	case "CHANGES_REQUESTED":
		return "requested changes"
	case "DISMISSED":
		return "review dismissed"
	default:
		return "reviewed"
	}
}

// waitSince is when the wait on a PR is counted from. elly stores no time for
// when a PR became someone's turn, so this is the latest activity by someone
// other than the user (a review, a comment, a thread reply), and the PR's last
// update when there is none. Commits have no login and are left out.
func waitSince(pr types.ViewPr, events []prEvent, username string) time.Time {
	var at time.Time
	for _, e := range events {
		if e.Login != "" && e.Login != username && !strings.HasSuffix(e.Login, "[bot]") && e.At.After(at) {
			at = e.At
		}
	}
	if at.IsZero() {
		return pr.LastUpdated
	}
	return at
}

// bucket names a wait in one cool-to-hot word.
func bucket(d time.Duration) string {
	switch {
	case d < 2*time.Hour:
		return "new"
	case d < 24*time.Hour:
		return "today"
	case d < 7*24*time.Hour:
		return "days"
	default:
		return "week+"
	}
}

func duration(d time.Duration) string {
	switch {
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + " min"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d.Hours())) + " h"
	default:
		return strconv.Itoa(int(d.Hours()/24)) + " d"
	}
}

// history is the last few events, oldest first.
func history(events []prEvent) []nextEvent {
	const keep = 6
	if len(events) > keep {
		events = events[len(events)-keep:]
	}
	out := make([]nextEvent, 0, len(events))
	for _, e := range events {
		out = append(out, nextEvent{At: e.At.Local().Format("Jan 2 15:04"), What: e.What})
	}
	return out
}

// reasonsByWeight strips the "+80: " prefix points puts on each reason and
// orders them by the size of their weight, largest first.
func reasonsByWeight(reasons []string) []string {
	type weighted struct {
		w    int
		text string
	}
	ws := make([]weighted, 0, len(reasons))
	for _, r := range reasons {
		num, text, ok := strings.Cut(r, ": ")
		w, err := strconv.Atoi(num)
		if !ok || err != nil {
			ws = append(ws, weighted{0, r})
			continue
		}
		if w < 0 {
			w = -w
		}
		ws = append(ws, weighted{w, text})
	}
	slices.SortStableFunc(ws, func(a, b weighted) int { return b.w - a.w })
	out := make([]string, 0, len(ws))
	for _, w := range ws {
		out = append(out, w.text)
	}
	return out
}

// reviewTag is the claude-review annotation as a short tag.
func reviewTag(pr types.ViewPr) *nextTag {
	for _, a := range pr.Annotations {
		if a.Source != points.ClaudeReviewSource {
			continue
		}
		switch a.State {
		case types.AnnotationRunning:
			return &nextTag{"●", "running", "running"}
		case types.AnnotationStuck:
			return &nextTag{"!", "stuck", "stuck"}
		case types.AnnotationNeedsYou:
			return &nextTag{"?", "needs you", "needs-you"}
		case types.AnnotationHandled:
			return &nextTag{"✓", "handled", "handled"}
		}
	}
	return nil
}

func stateFacts(pr types.ViewPr, username string) []string {
	var out []string
	switch {
	case len(pr.ReviewRequestedFromUsers) > 0:
		out = append(out, "review requested: "+strings.Join(pr.ReviewRequestedFromUsers, ", "))
	case pr.Author == username && username != "":
		out = append(out, "no reviewer requested")
	}
	if len(pr.RereviewFrom) > 0 {
		out = append(out, "reviewed before the latest change: "+strings.Join(pr.RereviewFrom, ", "))
	}
	if len(pr.ReviewedBy) > 0 {
		out = append(out, "reviewed by "+strings.Join(pr.ReviewedBy, ", "))
	}
	switch pr.ReviewStatus {
	case "APPROVED":
		out = append(out, "approved")
	case "CHANGES_REQUESTED":
		out = append(out, "changes requested")
	}
	switch {
	case pr.ChecksRed() && len(pr.ChecksFailing) == 0:
		out = append(out, "CI red, check names unavailable")
	case pr.ChecksRed() && !pr.ChecksComplete:
		out = append(out, "CI red, at least: "+strings.Join(pr.ChecksFailing, ", "))
	case pr.ChecksRed():
		out = append(out, "CI red: "+strings.Join(pr.ChecksFailing, ", "))
	case pr.ChecksState == "SUCCESS":
		out = append(out, "CI green")
	case pr.ChecksState == "PENDING" || pr.ChecksState == "EXPECTED":
		out = append(out, "CI running")
	case pr.ChecksState == "":
		out = append(out, "no checks")
	}
	if pr.HasConflict() {
		out = append(out, "conflicts with the base branch")
	} else if pr.MergeStateStatus == "BEHIND" {
		out = append(out, "behind the base branch")
	}
	if pr.ThreadsActionable > 0 {
		out = append(out, strconv.Itoa(pr.ThreadsActionable)+" threads waiting on you")
	}
	if pr.ThreadsWaiting > 0 {
		out = append(out, strconv.Itoa(pr.ThreadsWaiting)+" threads waiting on others")
	}
	if pr.IsDraft {
		out = append(out, "draft")
	}
	if pr.LastPrCommenter != "" {
		out = append(out, "last comment by "+pr.LastPrCommenter)
	}
	for _, a := range pr.Annotations {
		note := a.Source + ": " + strings.ReplaceAll(a.State, "_", " ")
		if a.Note != "" {
			note += ", " + a.Note
		}
		out = append(out, note)
	}
	return out
}
