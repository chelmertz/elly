package notify

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/chelmertz/elly/internal/types"
	"pgregory.net/rapid"
)

const propUser = "me"

var propWork = []string{"work"}

// genPr draws a PR whose fields cover every branch keys() looks at.
func genPr(t *rapid.T, label string) types.ViewPr {
	people := []string{propUser, "alice", "bob"}
	return types.ViewPr{
		Url:                      fmt.Sprintf("%s-%d", label, rapid.IntRange(0, 5).Draw(t, label+"url")),
		RepoOwner:                rapid.SampledFrom([]string{"work", "oss"}).Draw(t, label+"owner"),
		Author:                   rapid.SampledFrom(people).Draw(t, label+"author"),
		ReviewStatus:             rapid.SampledFrom([]string{"", "APPROVED", "CHANGES_REQUESTED", "REVIEW_REQUIRED"}).Draw(t, label+"status"),
		ThreadsActionable:        rapid.IntRange(0, 3).Draw(t, label+"actionable"),
		LastPrCommenter:          rapid.SampledFrom(append(people, "")).Draw(t, label+"commenter"),
		ReviewRequestedFromUsers: rapid.SliceOfDistinct(rapid.SampledFrom(people), rapid.ID[string]).Draw(t, label+"requested"),
		RereviewFrom:             rapid.SliceOfDistinct(rapid.SampledFrom(people), rapid.ID[string]).Draw(t, label+"rereview"),
		HeadRefOid:               rapid.SampledFrom([]string{"", "a", "b"}).Draw(t, label+"head"),
		Buried:                   rapid.Bool().Draw(t, label+"buried"),
		LastUpdated:              time.Unix(rapid.Int64Range(0, 3).Draw(t, label+"updated"), 0),
	}
}

func genPrs(t *rapid.T, label string) []types.ViewPr {
	n := rapid.IntRange(0, 6).Draw(t, label+"n")
	byUrl := map[string]types.ViewPr{}
	for i := range n {
		pr := genPr(t, fmt.Sprintf("%s%d", label, i))
		byUrl[pr.Url] = pr
	}
	prs := make([]types.ViewPr, 0, len(byUrl))
	for _, pr := range byUrl {
		prs = append(prs, pr)
	}
	return prs
}

func TestPropFirstRunNeverNotifies(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		if got, _ := Diff(nil, genPrs(t, "p"), propUser, propWork); len(got) != 0 {
			t.Fatalf("first run notified %v", got)
		}
	})
}

func TestPropSameFetchTwiceNotifiesNothing(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		prs := genPrs(t, "p")
		_, seen := Diff(map[string][]string{}, prs, propUser, propWork)
		if got, _ := Diff(seen, prs, propUser, propWork); len(got) != 0 {
			t.Fatalf("an unchanged fetch notified %v", got)
		}
	})
}

func TestPropNotifiesOnlyListedUnburiedPrsOnce(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		before, after := genPrs(t, "a"), genPrs(t, "b")
		_, seen := Diff(map[string][]string{}, before, propUser, propWork)
		got, next := Diff(seen, after, propUser, propWork)

		var urls []string
		for _, n := range got {
			i := slices.IndexFunc(after, func(p types.ViewPr) bool { return p.Url == n.PrUrl })
			if i < 0 || after[i].Buried {
				t.Fatalf("notified %q, which is not a listed unburied PR", n.PrUrl)
			}
			if slices.Contains(urls, n.PrUrl) {
				t.Fatalf("two notifications for %q", n.PrUrl)
			}
			urls = append(urls, n.PrUrl)
		}
		for url := range next {
			i := slices.IndexFunc(after, func(p types.ViewPr) bool { return p.Url == url })
			if i < 0 || after[i].Buried {
				t.Fatalf("remembered %q, which is not a listed unburied PR", url)
			}
		}
	})
}

func TestPropUsersOwnActionsNeverNotifyOnOpenSource(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		pr := genPr(t, "p")
		pr.RepoOwner, pr.Author, pr.Buried = "oss", propUser, false
		_, seen := Diff(map[string][]string{}, []types.ViewPr{pr}, propUser, propWork)

		pr.HeadRefOid += "-pushed"
		pr.LastUpdated = pr.LastUpdated.Add(time.Hour)
		pr.LastPrCommenter = propUser
		if got, _ := Diff(seen, []types.ViewPr{pr}, propUser, propWork); len(got) != 0 {
			t.Fatalf("the user's own push and comment notified %v", got)
		}
	})
}

func TestPropCoalesceNeverSendsMoreThanTheLimit(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(0, 20).Draw(t, "n")
		ns := make([]Notification, n)
		got := Coalesce(ns)
		if len(got) > maxPerFetch || (n > 0 && len(got) == 0) {
			t.Fatalf("%d notifications became %d", n, len(got))
		}
	})
}
