package points

import (
	"testing"
	"time"

	"github.com/chelmertz/elly/internal/types"
	"strings"
)

/* fuzz points:
all else equal: lower loc == higher points
*/

func absSum(a, b int) int {
	if a < 0 {
		a = -a
	}

	if b < 0 {
		b = -b
	}

	return a + b
}

func Fuzz_LowerLoc_HigherPoints(f *testing.F) {
	f.Add(2, 3, 5, 3, true)
	f.Fuzz(func(t *testing.T, pr1add int, pr1del int, pr2add int, pr2del int, prIsAuthoredByCurrentUser bool) {
		author := "author"
		if prIsAuthoredByCurrentUser {
			author = "currentUser"
		}
		pr1points := StandardPrPoints(types.ViewPr{Additions: pr1add, Deletions: pr1del, Author: author}, "currentUser", time.Now())
		pr1diff := absSum(pr1add, pr1del)

		pr2points := StandardPrPoints(types.ViewPr{Additions: pr2add, Deletions: pr2del, Author: author}, "currentUser", time.Now())
		pr2diff := absSum(pr2add, pr2del)

		if pr1diff > pr2diff && pr1points.Total > pr2points.Total ||
			pr2diff > pr1diff && pr2points.Total > pr1points.Total {
			t.Fatalf("lower loc should have higher points\nauthored by current user: %v\npr1diff: %d, pr1points: %+v\npr2diff: %d, pr2points: %+v", prIsAuthoredByCurrentUser, pr1diff, pr1points, pr2diff, pr2points)
		}
	})
}

func Test_StandardPrPoints(t *testing.T) {
	tests := []struct {
		name string
		pr   types.ViewPr
		now  time.Time
		want int
	}{
		{
			name: "new prs hints about adding reviewers",
			pr:   types.ViewPr{Author: "currentUser", LastUpdated: time.Now()},
			now:  time.Now(),
			want: 10,
		},
		{
			name: "inactive prs are scored as 0 before they get any interaction",
			pr:   types.ViewPr{Author: "currentUser", LastUpdated: time.Now(), ReviewRequestedFromUsers: []string{"otherUser"}},
			now:  time.Now(),
			want: 0,
		},
		{
			name: "inactive prs are bumped after a while",
			pr:   types.ViewPr{Author: "currentUser", LastUpdated: time.Now().Add(-15 * 24 * time.Hour), ReviewRequestedFromUsers: []string{"otherUser"}},
			now:  time.Now(),
			want: 11,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := StandardPrPoints(test.pr, "currentUser", test.now)
			if got.Total != test.want {
				t.Errorf("StandardPrPoints(%+v, time.Now()) = %+v, want %+v, got %+v", test.pr, test.now, test.want, got)
			}
		})
	}
}

func Test_RereviewPending_AddsPointsOnOwnPr(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	pr := types.ViewPr{Author: "me", Url: "https://github.com/o/r/pull/1", LastUpdated: now, ReviewRequestedFromUsers: []string{"adam"}, RereviewFrom: []string{"adam"}}
	p := StandardPrPoints(pr, "me", now)
	found := false
	for _, r := range p.Reasons {
		if strings.Contains(r, "Ask adam to re-review") {
			found = true
		}
	}
	if !found || p.Total < 20 {
		t.Fatalf("expected a re-review reason worth 20 points, got %d %v", p.Total, p.Reasons)
	}
	pr.RereviewFrom = []string{}
	if q := StandardPrPoints(pr, "me", now); q.Total != p.Total-20 {
		t.Fatalf("without the signal the PR must score 20 less: %d vs %d", q.Total, p.Total)
	}
}

// A red PR of ours is work only we can do, and it is the state in which asking
// anyone for a review wastes their time. It has to outrank the small nudges.
func Test_RedChecks_AddPointsOnOwnPr(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	pr := types.ViewPr{
		Author: "me", Url: "https://github.com/o/r/pull/1", LastUpdated: now,
		ReviewRequestedFromUsers: []string{"adam"},
		ChecksState:              "FAILURE",
		ChecksFailing:            []string{"scan (infra/apidocs)", "Infra PR Check"},
		ChecksComplete:           true,
	}
	red := StandardPrPoints(pr, "me", now)
	found := false
	for _, r := range red.Reasons {
		if strings.Contains(r, "CI is failing") && strings.Contains(r, "Infra PR Check") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a failing-CI reason naming the checks, got %v", red.Reasons)
	}

	green := pr
	green.ChecksState, green.ChecksFailing = "SUCCESS", nil
	if q := StandardPrPoints(green, "me", now); q.Total != red.Total-50 {
		t.Fatalf("a green PR must score 50 less than the same PR red: %d vs %d", q.Total, red.Total)
	}

	// Still running is not failing: nothing is owed until it lands.
	pending := pr
	pending.ChecksState, pending.ChecksFailing = "PENDING", nil
	if q := StandardPrPoints(pending, "me", now); q.Total != red.Total-50 {
		t.Fatalf("pending checks must not score as red: %d vs %d", q.Total, red.Total)
	}

	// Someone else's red PR is not our action, so this rule must not fire.
	theirs := pr
	theirs.Author = "adam"
	for _, r := range StandardPrPoints(theirs, "me", now).Reasons {
		if strings.Contains(r, "CI is failing") {
			t.Fatalf("the rule must not fire on someone else's PR: %v", r)
		}
	}
}

// A truncated list must read as a lower bound rather than an exact count.
func Test_RedChecks_TruncatedListSaysAtLeast(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	pr := types.ViewPr{
		Author: "me", Url: "https://github.com/o/r/pull/1", LastUpdated: now,
		ChecksState: "FAILURE", ChecksFailing: []string{"a", "b"}, ChecksComplete: false,
	}
	found := false
	for _, r := range StandardPrPoints(pr, "me", now).Reasons {
		if strings.Contains(r, "at least 2") {
			found = true
		}
	}
	if !found {
		t.Fatalf("an incomplete list must be reported as a lower bound: %v", StandardPrPoints(pr, "me", now).Reasons)
	}
}

// A conflicting PR is the case every other signal misses: checks are green,
// no thread is open, review is requested, and it still cannot merge. It was
// reported as ready on 2026-09-18 for exactly that reason.
func Test_MergeConflict_AddPointsOnOwnPr(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	pr := types.ViewPr{
		Author: "me", Url: "https://github.com/o/r/pull/1", LastUpdated: now,
		ReviewRequestedFromUsers: []string{"adam"},
		ChecksState:              "SUCCESS",
		ChecksComplete:           true,
		Mergeable:                "CONFLICTING",
		MergeStateStatus:         "DIRTY",
	}
	conflicting := StandardPrPoints(pr, "me", now)
	found := false
	for _, r := range conflicting.Reasons {
		if strings.Contains(r, "Merge conflict") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a merge-conflict reason, got %v", conflicting.Reasons)
	}

	clean := pr
	clean.Mergeable, clean.MergeStateStatus = "MERGEABLE", "CLEAN"
	if q := StandardPrPoints(clean, "me", now); q.Total != conflicting.Total-50 {
		t.Fatalf("a mergeable PR must score 50 less than the same PR conflicting: %d vs %d", q.Total, conflicting.Total)
	}

	// UNKNOWN is github still computing the merge, which happens on every poll
	// straight after a push. Scoring it would flag healthy PRs.
	unknown := pr
	unknown.Mergeable, unknown.MergeStateStatus = "UNKNOWN", "UNKNOWN"
	if q := StandardPrPoints(unknown, "me", now); q.Total != conflicting.Total-50 {
		t.Fatalf("an unknown mergeability must not score as a conflict: %d vs %d", q.Total, conflicting.Total)
	}

	// Someone else's conflict is theirs to rebase.
	theirs := pr
	theirs.Author = "adam"
	for _, r := range StandardPrPoints(theirs, "me", now).Reasons {
		if strings.Contains(r, "Merge conflict") {
			t.Fatalf("the rule must not fire on someone else's PR: %v", r)
		}
	}
}

// Someone else's PR that we reviewed, where the author has since answered.
// matchi-frontend#1995 on 2026-09-18: five open threads of ours, the author
// replied with a direct question and offered to push either change, and elly
// scored the PR -10 - the ThreadsWaiting penalty with nothing to offset it,
// because "someone else commented last" was fenced inside the own-PR branch.
// Nothing in the list said the PR needed us; it was noticed by hand.
func Test_TheyAnsweredOurReview_AddsPoints(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	reviewed := types.ViewPr{
		Author: "mirrenil", Url: "https://github.com/o/r/pull/1995", LastUpdated: now,
		ChecksState: "SUCCESS", ChecksComplete: true,
		ThreadsWaiting:  5,
		LastPrCommenter: "mirrenil",
	}

	answered := StandardPrPoints(reviewed, "me", now)
	found := false
	for _, r := range answered.Reasons {
		if strings.Contains(r, "mirrenil") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a reason naming the author who answered, got %v", answered.Reasons)
	}

	// The same PR where we spoke last is waiting on them, not on us.
	ours := reviewed
	ours.LastPrCommenter = "me"
	if q := StandardPrPoints(ours, "me", now); q.Total != answered.Total-10 {
		t.Fatalf("a PR we spoke last on must score 10 less: %d vs %d", q.Total, answered.Total)
	}

	// A PR we merely commented on once, with no thread of ours open, is not a
	// review anyone is waiting on - the involves: search is full of these.
	driveBy := reviewed
	driveBy.ThreadsWaiting = 0
	for _, r := range StandardPrPoints(driveBy, "me", now).Reasons {
		if strings.Contains(r, "mirrenil") {
			t.Fatalf("the rule must not fire without a thread of ours: %v", r)
		}
	}

	// Own PRs keep the behaviour they already had.
	mine := reviewed
	mine.Author, mine.ThreadsWaiting = "me", 0
	minePoints := StandardPrPoints(mine, "me", now)
	found = false
	for _, r := range minePoints.Reasons {
		if strings.Contains(r, "mirrenil") {
			found = true
		}
	}
	if !found {
		t.Fatalf("own PR must still score someone else commenting last: %v", minePoints.Reasons)
	}
}
