package main

import (
	"fmt"
	"math"
	"sort"
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

// standardPrPoints() awards points to PRs based on a set of rules.
// These rules should be revisited often, and the points should be tweaked.
func standardPrPoints(pr ViewPr, username string) *Points {
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
			points.Remove(10, "PR is someone else's draft")
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

	if pr.UnrespondedThreads > 0 {
		points.Add(80, fmt.Sprintf("Someone asked us something (%d comments)", pr.UnrespondedThreads))
		// we already need to go over this, don't scale the points
		// by amount of threads though, it might go overboard
	}

	sort.Slice(points.Reasons, func(i, j int) bool {
		// render all + points first, then - points
		return points.Reasons[i] < points.Reasons[j]
	})

	return points
}
