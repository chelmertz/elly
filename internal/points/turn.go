package points

import (
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/chelmertz/elly/internal/types"
)

// Group is where a PR goes on the /next page, decided by whose turn it is.
// The order of the constants is the page order. Points only order PRs inside a
// group: a high score never lifts a PR into a higher group.
type Group int

const (
	GroupOneClick Group = iota
	GroupWaiting
	GroupUnblock
	GroupWithClaude
	GroupOnOthers
	GroupNobody
	GroupIcebox
	GroupBuried
)

// Groups is every group, in page order.
var Groups = []Group{GroupOneClick, GroupWaiting, GroupUnblock, GroupWithClaude, GroupOnOthers, GroupNobody, GroupIcebox, GroupBuried}

func (g Group) Name() string {
	return [...]string{"One click from done", "Waiting for you", "Yours to unblock", "With claude · handed over, nothing for you", "On others", "Nobody's turn", "Icebox · untouched 2+ weeks", "Buried"}[g]
}

// Slug is the group's anchor on the page.
func (g Group) Slug() string {
	return [...]string{"one-click", "waiting", "unblock", "with-claude", "on-others", "nobody", "icebox", "buried"}[g]
}

// OnYou is true for the groups where the next move is the user's.
func (g Group) OnYou() bool {
	return g <= GroupUnblock
}

// Folded groups start collapsed on the page.
func (g Group) Folded() bool {
	return g > GroupUnblock
}

// Turn is the decision for one PR: its group, the one action the page offers,
// and why, in words.
type Turn struct {
	Group  Group
	Verb   string
	Reason string
	// VerbUrl is an annotation link the verb follows instead of the PR, or
	// "". It always names this PR (types.LinkNamesPr), and the page still
	// checks it against the link allowlist.
	VerbUrl string
}

// icebox is how long nobody may touch a PR before it is iced.
const icebox = 14 * 24 * time.Hour

// TurnOf decides whose turn a PR is. First match wins, and the order follows
// the pr-status skill: a PR that is red or owes a reply never comes out as
// waiting on someone else. Choices where the fields leave it open:
//
//   - An annotation link to this PR labelled Approve (or ending in /approve)
//     makes it one click from done, unless CI is red: a tool's verdict does
//     not outrank a red build, which is the author's to fix.
//   - A needs_you or stuck annotation, or an approve link, keeps a PR out of the
//     icebox: an annotation lives at most 24 hours, so a tool looked at it
//     recently even when github shows no activity.
//   - The icebox is checked before everything else on purpose, own PRs and
//     owed replies included, since the rule is "nobody touched it for two
//     weeks". The one exception is a review requested from the user: being
//     asked does not expire.
//   - A draft is nobody's turn, also the user's own draft and also when it
//     is approved: a draft asks nothing of anyone yet.
//   - CI still running is nobody's turn, even on a PR the user was asked to
//     review: the result decides whose move it is.
//   - On the user's own PR, reviewers who reviewed before the latest push
//     (RereviewFrom), and no reviewers at all, are the user's to unblock.
//   - A handled or running annotation drops owed replies and review
//     requests, as it does in the score: the user handed the PR over. From
//     claude-review, on someone else's PR, it goes to With claude, after a
//     red build (the author's to fix) and before CI still running; from any
//     other tool the author then has the next move.
//   - A stuck annotation is needs_you that nothing but the user will move.
//   - With no username (no token yet), no PR is the user's own.
func TurnOf(pr types.ViewPr, username string, now time.Time) Turn {
	own := username != "" && pr.Author == username
	asked := !own && username != "" &&
		(slices.Contains(pr.ReviewRequestedFromUsers, username) || slices.Contains(pr.RereviewFrom, username))
	needsYou, approveUrl := annotationVerdict(pr)
	handled := needsYou == nil && HandedOver(pr, "")
	withClaude := needsYou == nil && !own && HandedOver(pr, ClaudeReviewSource)

	switch {
	case pr.Buried:
		return Turn{Group: GroupBuried, Verb: "Open", Reason: "you buried it"}
	case approveUrl != "" && !pr.ChecksRed():
		return Turn{Group: GroupOneClick, Verb: "Approve", Reason: "a tool says it is ready to approve", VerbUrl: approveUrl}
	case needsYou != nil:
		t := Turn{Group: GroupWaiting, Verb: "Check", Reason: needsYou.Source + " needs you"}
		if needsYou.State == types.AnnotationStuck {
			t.Reason = needsYou.Source + " is stuck: only you can move it"
		}
		for _, l := range needsYou.Links {
			if types.LinkNamesPr(pr.Url, l.Url) {
				t.VerbUrl = l.Url
				break
			}
		}
		return t
	case !asked && pr.LastUpdated.Before(now.Add(-icebox)):
		return Turn{Group: GroupIcebox, Verb: "Open", Reason: "nobody has touched it for two weeks"}
	case pr.IsDraft:
		return Turn{Group: GroupNobody, Verb: "Open", Reason: "a draft"}
	case own && pr.HasConflict():
		return Turn{Group: GroupUnblock, Verb: "Rebase", Reason: "yours, and it conflicts with the base branch"}
	case own && pr.ChecksRed():
		return Turn{Group: GroupUnblock, Verb: "Fix CI", Reason: "yours, and CI is red"}
	case pr.ChecksRed():
		return Turn{Group: GroupOnOthers, Verb: "Open", Reason: "CI is red, the author's to fix"}
	case withClaude:
		t := Turn{Group: GroupWithClaude, Verb: "Open", Reason: "handed over to " + ClaudeReviewSource + ", nothing for you"}
		if u := attachUrl(pr); u != "" {
			t.Verb, t.VerbUrl = "Attach", u
		}
		return t
	case pr.ThreadsActionable > 0 && !handled:
		return Turn{Group: GroupWaiting, Verb: "Reply", Reason: "you owe a reply"}
	case own && pr.ReviewStatus == "CHANGES_REQUESTED":
		return Turn{Group: GroupUnblock, Verb: "Address", Reason: "yours, and changes are requested"}
	case pr.ChecksState == "PENDING" || pr.ChecksState == "EXPECTED":
		return Turn{Group: GroupNobody, Verb: "Open", Reason: "CI is still running"}
	case own && pr.ReviewStatus == "APPROVED":
		return Turn{Group: GroupOneClick, Verb: "Merge", Reason: "yours and approved"}
	case own && len(pr.RereviewFrom) > 0:
		return Turn{Group: GroupUnblock, Verb: "Re-request", Reason: "yours, reviewed before your latest change: ask " + strings.Join(pr.RereviewFrom, ", ") + " again"}
	case own && len(pr.ReviewRequestedFromUsers) == 0 && len(pr.ReviewedBy) == 0:
		return Turn{Group: GroupUnblock, Verb: "Add reviewer", Reason: "yours, and nobody is asked to review it"}
	case own:
		return Turn{Group: GroupOnOthers, Verb: "Open", Reason: "yours, waiting on reviewers"}
	case asked && !handled:
		return Turn{Group: GroupWaiting, Verb: "Review", Reason: "a review is asked of you"}
	case pr.ThreadsWaiting > 0:
		return Turn{Group: GroupOnOthers, Verb: "Open", Reason: "the author owes replies to your threads"}
	case handled:
		return Turn{Group: GroupOnOthers, Verb: "Open", Reason: "a tool took over your part"}
	default:
		return Turn{Group: GroupNobody, Verb: "Open", Reason: "nobody asked you for anything"}
	}
}

// ClaudeReviewSource is the source claude-review writes its annotations
// under (annotationSource in its elly.go); its handed-over PRs get a group.
const ClaudeReviewSource = "claude-review"

// annotationVerdict returns the first needs_you or stuck annotation, and the
// first link of any annotation that approves this PR. A link that approves
// another PR is no verdict on this one.
func annotationVerdict(pr types.ViewPr) (needsYou *types.Annotation, approveUrl string) {
	for i, a := range pr.Annotations {
		if (a.State == types.AnnotationNeedsYou || a.State == types.AnnotationStuck) && needsYou == nil {
			needsYou = &pr.Annotations[i]
		}
		for _, l := range a.Links {
			if approveUrl == "" && approves(l) && types.LinkNamesPr(pr.Url, l.Url) {
				approveUrl = l.Url
			}
		}
	}
	return needsYou, approveUrl
}

func approves(l types.AnnotationLink) bool { return linkDoes(l, "approve") }

// attachUrl is the first link of claude-review's annotation that attaches to
// its session, or "".
func attachUrl(pr types.ViewPr) string {
	for _, a := range pr.Annotations {
		if a.Source != ClaudeReviewSource {
			continue
		}
		for _, l := range a.Links {
			if linkDoes(l, "attach") && types.LinkNamesPr(pr.Url, l.Url) {
				return l.Url
			}
		}
	}
	return ""
}

// linkDoes: the link is labelled verb, or its path ends in /verb.
func linkDoes(l types.AnnotationLink, verb string) bool {
	if strings.EqualFold(strings.TrimSpace(l.Label), verb) {
		return true
	}
	u, err := url.Parse(l.Url)
	return err == nil && strings.EqualFold(u.Path[strings.LastIndexByte(u.Path, '/')+1:], verb)
}

// Rank orders PRs as the /next page does: by group, then by points, then
// most recently updated first. It returns each URL's position.
func Rank(prs []types.ViewPr, username string, now time.Time) map[string]int {
	type ranked struct {
		url     string
		group   Group
		total   int
		updated time.Time
	}
	rs := make([]ranked, 0, len(prs))
	for _, pr := range prs {
		rs = append(rs, ranked{pr.Url, TurnOf(pr, username, now).Group, StandardPrPoints(pr, username, now).Total, pr.LastUpdated})
	}
	slices.SortStableFunc(rs, func(a, b ranked) int {
		switch {
		case a.group != b.group:
			return int(a.group) - int(b.group)
		case a.total != b.total:
			return b.total - a.total
		default:
			return b.updated.Compare(a.updated)
		}
	})
	pos := make(map[string]int, len(rs))
	for i, r := range rs {
		pos[r.url] = i
	}
	return pos
}
