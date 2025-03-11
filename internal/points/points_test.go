package points

import (
	"testing"
	"time"

	"github.com/chelmertz/elly/internal/types"
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
