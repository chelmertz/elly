package points

import (
	"testing"
	"time"

	"github.com/chelmertz/elly/internal/types"
)

func TestTurnOf(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	const me = "me"
	fresh := now.Add(-time.Hour)
	old := now.Add(-15 * 24 * time.Hour)
	approveLink := []types.AnnotationLink{{Label: "Approve", Url: "claude-review://o/r/1/approve"}}

	cases := []struct {
		name string
		pr   types.ViewPr
		want Group
		verb string
	}{
		{"buried beats everything", types.ViewPr{Author: me, ReviewStatus: "APPROVED", Buried: true, LastUpdated: fresh}, GroupBuried, "Open"},
		{"own approved is a merge", types.ViewPr{Author: me, ReviewStatus: "APPROVED", ReviewedBy: []string{"a"}, LastUpdated: fresh}, GroupOneClick, "Merge"},
		{"own approved but red is unblock", types.ViewPr{Author: me, ReviewStatus: "APPROVED", ChecksState: "FAILURE", LastUpdated: fresh}, GroupUnblock, "Fix CI"},
		{"own approved but conflicting is unblock", types.ViewPr{Author: me, ReviewStatus: "APPROVED", Mergeable: "CONFLICTING", LastUpdated: fresh}, GroupUnblock, "Rebase"},
		{"own approved while CI runs is nobody's", types.ViewPr{Author: me, ReviewStatus: "APPROVED", ChecksState: "PENDING", LastUpdated: fresh}, GroupNobody, "Open"},
		{"own changes requested is unblock", types.ViewPr{Author: me, ReviewStatus: "CHANGES_REQUESTED", ReviewedBy: []string{"a"}, LastUpdated: fresh}, GroupUnblock, "Address"},
		{"own with no reviewers is unblock", types.ViewPr{Author: me, LastUpdated: fresh}, GroupUnblock, "Add reviewer"},
		{"own with stale reviewers is unblock", types.ViewPr{Author: me, RereviewFrom: []string{"a"}, ReviewedBy: []string{"a"}, LastUpdated: fresh}, GroupUnblock, "Re-request"},
		{"own waiting on reviewers is on others", types.ViewPr{Author: me, ReviewRequestedFromUsers: []string{"a"}, LastUpdated: fresh}, GroupOnOthers, "Open"},
		{"own draft is nobody's", types.ViewPr{Author: me, IsDraft: true, ReviewStatus: "APPROVED", LastUpdated: fresh}, GroupNobody, "Open"},
		{"reply owed on someone else's PR", types.ViewPr{Author: "a", ThreadsActionable: 2, LastUpdated: fresh}, GroupWaiting, "Reply"},
		{"reply owed on own PR", types.ViewPr{Author: me, ThreadsActionable: 1, ReviewRequestedFromUsers: []string{"a"}, LastUpdated: fresh}, GroupWaiting, "Reply"},
		{"review requested", types.ViewPr{Author: "a", ReviewRequestedFromUsers: []string{me}, LastUpdated: fresh}, GroupWaiting, "Review"},
		{"re-review from me", types.ViewPr{Author: "a", RereviewFrom: []string{me}, LastUpdated: fresh}, GroupWaiting, "Review"},
		// the score gives a small PR +50; being merely involved is still nobody's turn
		{"involved only, however small", types.ViewPr{Author: "a", Additions: 1, LastUpdated: fresh}, GroupNobody, "Open"},
		{"a team request is not a request of me", types.ViewPr{Author: "a", ReviewRequestedFromUsers: []string{"@team"}, LastUpdated: fresh}, GroupNobody, "Open"},
		{"someone else's red PR is on them", types.ViewPr{Author: "a", ReviewRequestedFromUsers: []string{me}, ChecksState: "FAILURE", LastUpdated: fresh}, GroupOnOthers, "Open"},
		{"they owe replies to my threads", types.ViewPr{Author: "a", ThreadsWaiting: 3, LastUpdated: fresh}, GroupOnOthers, "Open"},
		{"needs_you annotation waits for me", types.ViewPr{Author: "a", Annotations: []types.Annotation{{Source: "claude-review", State: types.AnnotationNeedsYou}}, LastUpdated: fresh}, GroupWaiting, "Check"},
		{"approve link is one click", types.ViewPr{Author: "a", ReviewRequestedFromUsers: []string{me}, Annotations: []types.Annotation{{Source: "claude-review", State: types.AnnotationNeedsYou, Links: approveLink}}, LastUpdated: fresh}, GroupOneClick, "Approve"},
		{"approve link on a red PR is not one click", types.ViewPr{Author: "a", ChecksState: "FAILURE", Annotations: []types.Annotation{{Source: "claude-review", State: types.AnnotationNeedsYou, Links: approveLink}}, LastUpdated: fresh}, GroupWaiting, "Check"},
		{"handled drops the review request", types.ViewPr{Author: "a", ReviewRequestedFromUsers: []string{me}, Annotations: []types.Annotation{{Source: "claude-review", State: types.AnnotationHandled}}, LastUpdated: fresh}, GroupOnOthers, "Open"},
		{"untouched two weeks is iced", types.ViewPr{Author: "a", ThreadsWaiting: 1, LastUpdated: old}, GroupIcebox, "Open"},
		{"own approved untouched two weeks is iced", types.ViewPr{Author: me, ReviewStatus: "APPROVED", LastUpdated: old}, GroupIcebox, "Open"},
		{"asked is never iced", types.ViewPr{Author: "a", ReviewRequestedFromUsers: []string{me}, LastUpdated: old}, GroupWaiting, "Review"},
		{"a live annotation is never iced", types.ViewPr{Author: "a", Annotations: []types.Annotation{{Source: "x", State: types.AnnotationNeedsYou}}, LastUpdated: old}, GroupWaiting, "Check"},
		{"buried stays buried when old", types.ViewPr{Author: "a", Buried: true, LastUpdated: old}, GroupBuried, "Open"},
		{"no username owns nothing", types.ViewPr{Author: "", ReviewStatus: "APPROVED", LastUpdated: fresh}, GroupNobody, "Open"},
	}
	for _, c := range cases {
		user := me
		if c.name == "no username owns nothing" {
			user = ""
		}
		got := TurnOf(c.pr, user, now)
		if got.Group != c.want || got.Verb != c.verb {
			t.Errorf("%s: got %s/%s, want %s/%s", c.name, got.Group.Name(), got.Verb, c.want.Name(), c.verb)
		}
	}
}

// A big score never lifts a PR into a higher group: a small PR nobody asked
// the user about outscores a big review the user was asked for, and still
// ranks after it.
func TestRankOrdersByGroupBeforePoints(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	prs := []types.ViewPr{
		// nobody's turn, scores +50 for being small
		{Url: "small", Author: "a", Additions: 1, LastUpdated: now},
		// waiting for me, scores +10 for a big review
		{Url: "asked", Author: "a", Additions: 1000, ReviewRequestedFromUsers: []string{"me"}, LastUpdated: now},
	}
	if StandardPrPoints(prs[0], "me", now).Total <= StandardPrPoints(prs[1], "me", now).Total {
		t.Fatal("the test needs the lower group to score higher")
	}
	pos := Rank(prs, "me", now)
	if pos["asked"] != 0 || pos["small"] != 1 {
		t.Errorf("got %v, want asked first", pos)
	}
}
