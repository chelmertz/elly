package points

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/chelmertz/elly/internal/types"
)

type Points struct {
	Total   int
	Reasons []string
}

func (p *Points) Add(points int, reason string) {
	p.Total += points
	reasonWithPrefix := fmt.Sprintf("+%d: %s", points, reason)
	p.Reasons = append(p.Reasons, reasonWithPrefix)
}

func (p *Points) Remove(points int, reason string) {
	p.Total -= points
	reasonWithPrefix := fmt.Sprintf("-%d: %s", points, reason)
	p.Reasons = append(p.Reasons, reasonWithPrefix)
}

// StandardPrPoints() awards points to PRs based on a set of rules.
// These rules should be revisited often, and the points should be tweaked.
func StandardPrPoints(pr types.ViewPr, username string, now time.Time) *Points {
	if now.IsZero() {
		panic("now is zero, please give StandardPrPoints() a valid time")
	}
	points := &Points{}
	points.Reasons = make([]string, 0)

	if pr.Author == username {
		// our pr
		if pr.ReviewStatus == "APPROVED" {
			points.Add(100, "Own PR is approved, should be a simple merge")
		}

		if pr.ReviewStatus == "CHANGES_REQUESTED" {
			points.Add(50, "Someone wants you to change something")
		}

		if pr.LastPrCommenter != "" && pr.LastPrCommenter != username {
			// someone might have asked us something
			points.Add(10, fmt.Sprintf("Someone else commented last (%s)", pr.LastPrCommenter))
		}

		if pr.IsDraft {
			points.Remove(10, "PR is my draft")
		}

		if len(pr.ReviewRequestedFromUsers) == 0 {
			// points represents "actions to take", and if there are no
			// reviewers assigned to your pr, there's a chance no-one will look
			// at it
			points.Add(10, "You should add reviewers")
		}

		if pr.LastUpdated.Before(now.Add(-14 * 24 * time.Hour)) {
			points.Add(11, "Your PR has not been updated in a while, you should take actions")
		}
	} else {
		// someone else's pr, or our but the username is not set
		if pr.ReviewStatus == "APPROVED" {
			points.Remove(100, "PR is someone else's and is approved")
		}

		if pr.ReviewStatus == "CHANGES_REQUESTED" {
			// you might want to wait with this, it seems like the PR author has
			// some work to do already
			points.Remove(100, "Changes are already requested")
		}

		if pr.IsDraft {
			if pr.LastUpdated.Before(now.Add(-5 * 24 * time.Hour)) {
				// another person's draft might be interesting if it's new, but
				// when the "draft" status is being used as a "WIP" status, it
				// probably doesn't require our immediate attention
				points.Remove(70, "PR is someone else's old draft")
			} else {
				points.Remove(10, "PR is someone else's draft")
			}
		}

		// reward short prs
		diff := int(math.Abs(float64(pr.Additions)) + math.Abs(float64(pr.Deletions)))
		switch {
		case diff < 50:
			points.Add(50, fmt.Sprintf("PR is small, %d loc changed is <50", diff))
		case diff < 150:
			points.Add(30, fmt.Sprintf("PR is smallish, %d loc changed is <150", diff))
		case diff <= 300:
			points.Add(20, fmt.Sprintf("PR is bigger, %d loc changed is <=300", diff))
		case diff > 300:
			points.Add(10, fmt.Sprintf("PR is bigish, %d loc changed is >300", diff))
		}
	}

	if pr.ThreadsActionable > 0 {
		points.Add(80, fmt.Sprintf("Someone asked us something, or reacted to our comment (%d comments)", pr.ThreadsActionable))
		// we already need to go over this, don't scale the points
		// by amount of threads though, it might go overboard
	}

	if pr.ThreadsWaiting > 0 {
		points.Remove(10, fmt.Sprintf("Someone should respond to our comments (%d comments)", pr.ThreadsWaiting))
	}

	sort.Slice(points.Reasons, func(i, j int) bool {
		// render all + points first, then - points
		return points.Reasons[i] < points.Reasons[j]
	})

	if pr.Buried {
		// TODO test that no other combinations of input can negate the effect of something buried
		points.Remove(1000, "PR is buried")
	}

	return points
}
