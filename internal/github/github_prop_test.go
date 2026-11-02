package github

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"
)

// Logins a thread or a review can come from: the user, two people, a bot and
// the CI commenters elly ignores. "" stands for a deleted account.
var propLogins = []string{"me", "adam", "eve", "dep[bot]", "github-actions", "vercel", ""}

// propReviewers leans toward people, so that a re-review is actually owed in
// a good share of the generated PRs rather than one in a thousand.
var propReviewers = []string{"adam", "eve", "adam", "eve", "me", "dep[bot]", "github-actions", "vercel", ""}

var propTime = time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)

func genStamp(t *rapid.T, label string) string {
	if rapid.IntRange(0, 9).Draw(t, label+"Missing") == 0 {
		return "" // unparseable, as a missing field would be
	}
	return propTime.Add(time.Duration(rapid.IntRange(-100, 100).Draw(t, label)) * time.Hour).Format(time.RFC3339)
}

func genComment(t *rapid.T, label string) prReviewThreadCommentGraphQl {
	c := commentBy(rapid.SampledFrom(propLogins).Draw(t, label))
	c.CreatedAt = genStamp(t, label+"At")
	for i := range rapid.IntRange(0, 2).Draw(t, label+"Reactions") {
		var e struct {
			Node struct {
				Content string
				User    struct{ Login string }
			}
		}
		e.Node.Content = "THUMBS_UP"
		e.Node.User.Login = rapid.SampledFrom(propLogins).Draw(t, fmt.Sprintf("%sReactor%d", label, i))
		c.Reactions.Edges = append(c.Reactions.Edges, e)
	}
	return c
}

// genThread is any thread the aliased query can return, including the
// comment-less one the types allow.
func genThread(t *rapid.T, i int) prReviewThreadGraphQl {
	l := fmt.Sprintf("thread%d", i)
	var th prReviewThreadGraphQl
	th.IsResolved = rapid.Bool().Draw(t, l+"Resolved")
	th.IsOutdated = rapid.Bool().Draw(t, l+"Outdated")
	th.IsCollapsed = rapid.IntRange(0, 4).Draw(t, l+"Collapsed") == 0
	if rapid.IntRange(0, 9).Draw(t, l+"Empty") > 0 {
		th.FirstComment.Nodes = []prReviewThreadCommentGraphQl{genComment(t, l+"First")}
		th.LastComment.Nodes = []prReviewThreadCommentGraphQl{genComment(t, l+"Last")}
	}
	return th
}

func genPr(t *rapid.T) prSearchResultGraphQl {
	var pr prSearchResultGraphQl
	pr.Author.Login = rapid.SampledFrom([]string{"me", "me", "adam"}).Draw(t, "author")
	pr.IsDraft = rapid.Bool().Draw(t, "draft")
	for i := range rapid.IntRange(0, 4).Draw(t, "threads") {
		pr.ReviewThreads.Edges = append(pr.ReviewThreads.Edges, struct{ Node prReviewThreadGraphQl }{genThread(t, i)})
	}
	if rapid.Bool().Draw(t, "hasCommit") {
		pr.Commits.Nodes = []prCommitNodeGraphQl{{}}
		pr.Commits.Nodes[0].Commit.Author.Date = genStamp(t, "commit")
	}
	for i := range rapid.IntRange(0, 2).Draw(t, "comments") {
		var e struct {
			Node struct {
				UpdatedAt string
				Author    struct{ Login string }
				Url       string
			}
		}
		e.Node.Author.Login = rapid.SampledFrom(propLogins).Draw(t, fmt.Sprintf("comment%d", i))
		e.Node.UpdatedAt = genStamp(t, fmt.Sprintf("comment%dAt", i))
		pr.Comments.Edges = append(pr.Comments.Edges, e)
	}
	for i := range rapid.IntRange(0, 2).Draw(t, "requested") {
		var n struct{ RequestedReviewer requestedReviewerGraphQl }
		n.RequestedReviewer.Login = rapid.SampledFrom(propLogins).Draw(t, fmt.Sprintf("requested%d", i))
		pr.ReviewRequests.Nodes = append(pr.ReviewRequests.Nodes, n)
	}
	for i := range rapid.IntRange(0, 5).Draw(t, "reviews") {
		var e struct {
			Node struct {
				Author                  struct{ Login string }
				Url, State, SubmittedAt string
			}
		}
		e.Node.Author.Login = rapid.SampledFrom(propReviewers).Draw(t, fmt.Sprintf("reviewer%d", i))
		e.Node.State = rapid.SampledFrom([]string{"COMMENTED", "CHANGES_REQUESTED", "APPROVED", "DISMISSED"}).Draw(t, fmt.Sprintf("state%d", i))
		e.Node.SubmittedAt = genStamp(t, fmt.Sprintf("review%dAt", i))
		pr.Reviews.Edges = append(pr.Reviews.Edges, e)
	}
	return pr
}

func TestPropThreadCountsAreBoundedByTheThreads(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		pr := genPr(t)
		a, w := actionableThreads(pr, "me")
		if a < 0 || w < 0 || a+w > len(pr.ReviewThreads.Edges) {
			t.Fatalf("%d actionable + %d waiting from %d threads", a, w, len(pr.ReviewThreads.Edges))
		}
	})
}

// A resolved, collapsed or comment-less thread is never counted: adding any
// number of them changes nothing.
func TestPropClosedThreadsNeverCount(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		pr := genPr(t)
		a, w := actionableThreads(pr, "me")
		for i := range rapid.IntRange(1, 3).Draw(t, "closed") {
			th := genThread(t, 100+i)
			switch rapid.IntRange(0, 2).Draw(t, fmt.Sprintf("why%d", i)) {
			case 0:
				th.IsResolved = true
			case 1:
				th.IsCollapsed = true
			default:
				th.FirstComment.Nodes, th.LastComment.Nodes = nil, nil
			}
			at := rapid.IntRange(0, len(pr.ReviewThreads.Edges)).Draw(t, fmt.Sprintf("at%d", i))
			pr.ReviewThreads.Edges = slices.Insert(pr.ReviewThreads.Edges, at, struct{ Node prReviewThreadGraphQl }{th})
		}
		if a2, w2 := actionableThreads(pr, "me"); a2 != a || w2 != w {
			t.Fatalf("closed threads moved the counts from %d/%d to %d/%d", a, w, a2, w2)
		}
	})
}

// A thread where I have the last word waits on someone else. The one
// exception is by design: on my own PR, someone else reacting to my last word
// (a thumbs-up on "done?") hands it back to me.
func TestPropMyLastWordIsNeverActionable(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		pr := genPr(t)
		th := genThread(t, 0)
		last := genComment(t, "mine")
		last.Author.Login = "me"
		th.LastComment.Nodes = []prReviewThreadCommentGraphQl{last}
		if len(th.FirstComment.Nodes) == 0 {
			th.FirstComment.Nodes = []prReviewThreadCommentGraphQl{genComment(t, "first")}
		}
		pr.ReviewThreads.Edges = []struct{ Node prReviewThreadGraphQl }{{th}}
		a, _ := actionableThreads(pr, "me")
		handedBack := pr.Author.Login == "me" && !th.IsOutdated && someoneElseReactedToComment(last.Reactions, "me")
		if a != 0 && !handedBack {
			t.Fatalf("my last word counted as actionable: %+v", th)
		}
	})
}

func isPerson(login string) bool {
	return login != "me" && login != "" && !strings.HasSuffix(login, "[bot]") && !slices.Contains(ignoredLastPrCommenters, login)
}

// Who reviewed and whom to ask again name people only, each once.
func TestPropReviewerListsNamePeopleOnce(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		pr := genPr(t)
		threadsActionable := rapid.SampledFrom([]int{0, 0, 0, 1, 2}).Draw(t, "threadsActionable")
		by := reviewedBy(pr, "me")
		again := rereviewFrom(pr, "me", threadsActionable)
		for name, list := range map[string][]string{"reviewedBy": by, "rereviewFrom": again} {
			for i, l := range list {
				if !isPerson(l) {
					t.Fatalf("%s names %q", name, l)
				}
				if slices.Contains(list[:i], l) {
					t.Fatalf("%s names %q twice: %v", name, l, list)
				}
			}
		}
		for _, l := range again {
			if !slices.Contains(by, l) {
				t.Fatalf("%q is asked to review again without having reviewed: %v", l, by)
			}
		}
		if (pr.IsDraft || threadsActionable > 0 || pr.Author.Login != "me") && len(again) != 0 {
			t.Fatalf("re-review asked on a draft, someone else's PR or with threads open: %v", again)
		}
	})
}
