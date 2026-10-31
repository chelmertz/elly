package points

import (
	"testing"
	"time"

	"github.com/chelmertz/elly/internal/types"
	"pgregory.net/rapid"
)

func genScoredPr(t *rapid.T) types.ViewPr {
	now := time.Unix(1_800_000_000, 0)
	return types.ViewPr{
		Author:                   rapid.SampledFrom([]string{"me", "other"}).Draw(t, "author"),
		ReviewStatus:             rapid.SampledFrom([]string{"", "APPROVED", "CHANGES_REQUESTED"}).Draw(t, "status"),
		ThreadsActionable:        rapid.IntRange(0, 3).Draw(t, "actionable"),
		ThreadsWaiting:           rapid.IntRange(0, 3).Draw(t, "waiting"),
		Additions:                rapid.IntRange(0, 1000).Draw(t, "add"),
		Deletions:                rapid.IntRange(0, 1000).Draw(t, "del"),
		IsDraft:                  rapid.Bool().Draw(t, "draft"),
		LastPrCommenter:          rapid.SampledFrom([]string{"", "me", "other"}).Draw(t, "commenter"),
		ReviewRequestedFromUsers: rapid.SliceOfN(rapid.SampledFrom([]string{"me", "x"}), 0, 2).Draw(t, "requested"),
		LastUpdated:              now.Add(-time.Duration(rapid.IntRange(0, 30*24).Draw(t, "hoursOld")) * time.Hour),
	}
}

var propNow = time.Unix(1_800_000_000, 0)

func TestPropNeedsYouAlwaysCountsUnlessBuried(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		pr := genScoredPr(t)
		pr.Annotations = []types.Annotation{{Source: "t", State: types.AnnotationNeedsYou}}
		if rapid.Bool().Draw(t, "alsoHandled") {
			pr.Annotations = append(pr.Annotations, types.Annotation{Source: "u", State: types.AnnotationHandled})
		}
		if p := StandardPrPoints(pr, "me", propNow); p.Total < needsYouPoints {
			t.Fatalf("needs_you scored %d: %v", p.Total, p.Reasons)
		}
		pr.Buried = true
		if p := StandardPrPoints(pr, "me", propNow); p.Total > 0 {
			t.Fatalf("buried needs_you scored %d", p.Total)
		}
	})
}

func TestPropHandledNeverRaisesTheScore(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		pr := genScoredPr(t)
		plain := StandardPrPoints(pr, "me", propNow).Total
		pr.Annotations = []types.Annotation{{Source: "t", State: types.AnnotationHandled}}
		if handled := StandardPrPoints(pr, "me", propNow).Total; handled > plain {
			t.Fatalf("handled raised the score from %d to %d", plain, handled)
		}
	})
}
