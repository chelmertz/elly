package main

import (
	"testing"
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
		pr1points := standardPrPoints(ViewPr{Additions: pr1add, Deletions: pr1del, Author: author}, "currentUser")
		pr1diff := absSum(pr1add, pr1del)

		pr2points := standardPrPoints(ViewPr{Additions: pr2add, Deletions: pr2del, Author: author}, "currentUser")
		pr2diff := absSum(pr2add, pr2del)

		if pr1diff > pr2diff && pr1points.Total > pr2points.Total ||
			pr2diff > pr1diff && pr2points.Total > pr1points.Total {
			t.Fatalf("lower loc should have higher points\nauthored by current user: %v\npr1diff: %d, pr1points: %+v\npr2diff: %d, pr2points: %+v", prIsAuthoredByCurrentUser, pr1diff, pr1points, pr2diff, pr2points)
		}
	})
}
