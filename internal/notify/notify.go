// Package notify decides which PR changes deserve a desktop notification and
// sends them.
//
// A notification fires when a PR gains a reason: a key below that was absent
// on the previous fetch. Keys name the kind of reason, never its text, so a
// thread count going from 1 to 3 does not fire again, while an approval that
// is dismissed and given again does.
package notify

import (
	"fmt"
	"html"
	"log/slog"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/chelmertz/elly/internal/types"
)

type Notification struct {
	PrUrl string
	Title string
	Body  string
}

// maxPerFetch is how many notifications one fetch may send before they are
// folded into one summary, e.g. the first fetch after a week offline.
const maxPerFetch = 3

// keys returns the reasons a PR has right now, each with the words a
// notification uses for it. Owners in workOwners get the filtered reasons
// only; any other owner, such as an open-source project, also gets a key per
// change by someone else, so every such change notifies.
func keys(pr types.ViewPr, username string, workOwners []string) map[string]string {
	k := make(map[string]string)
	own := pr.Author == username

	if pr.ThreadsActionable > 0 {
		k["replies"] = "Someone asked us something, or reacted to our comment"
	}
	if own {
		switch pr.ReviewStatus {
		case "APPROVED":
			k["approved"] = "Own PR is approved, should be a simple merge"
		case "CHANGES_REQUESTED":
			k["changes_requested"] = "Someone wants you to change something"
		}
		if pr.LastPrCommenter != "" && pr.LastPrCommenter != username {
			k["other_commented_last"] = pr.LastPrCommenter + " commented"
		}
	} else if slices.Contains(pr.ReviewRequestedFromUsers, username) || slices.Contains(pr.RereviewFrom, username) {
		k["review_requested"] = "Review requested from you"
	}

	if slices.Contains(workOwners, pr.RepoOwner) {
		return k
	}
	if !own && pr.HeadRefOid != "" {
		k["head:"+pr.HeadRefOid] = "New commits"
	}
	if pr.ReviewStatus != "" {
		k["review:"+pr.ReviewStatus] = "Review status: " + strings.ToLower(strings.ReplaceAll(pr.ReviewStatus, "_", " "))
	}
	if pr.LastPrCommenter != "" && pr.LastPrCommenter != username {
		k["comment:"+pr.LastUpdated.Format(time.RFC3339)] = pr.LastPrCommenter + " commented"
	}
	return k
}

// Diff compares the PRs of one fetch with the keys seen on the previous one.
// It returns one notification per PR that gained a key, and the keys to
// remember. A nil seen means nothing was ever recorded: the first run records
// a baseline and sends nothing. Buried PRs never notify.
func Diff(seen map[string][]string, prs []types.ViewPr, username string, workOwners []string) ([]Notification, map[string][]string) {
	next := make(map[string][]string, len(prs))
	var out []Notification
	for _, pr := range prs {
		if pr.Buried {
			continue
		}
		k := keys(pr, username, workOwners)
		var gained []string
		for key, words := range k {
			next[pr.Url] = append(next[pr.Url], key)
			if seen != nil && !slices.Contains(seen[pr.Url], key) {
				gained = append(gained, words)
			}
		}
		slices.Sort(next[pr.Url])
		if len(gained) > 0 {
			slices.Sort(gained)
			out = append(out, Notification{PrUrl: pr.Url, Title: pr.Title, Body: strings.Join(gained, "\n")})
		}
	}
	return out, next
}

// Coalesce folds more than maxPerFetch notifications into one summary.
func Coalesce(ns []Notification) []Notification {
	if len(ns) <= maxPerFetch {
		return ns
	}
	return []Notification{{Title: "elly", Body: fmt.Sprintf("%d PRs changed", len(ns))}}
}

// escape makes text safe for a notification daemon that renders markup, such
// as dunst with markup = "full": a PR title must not restyle the card.
func escape(s string) string {
	return html.EscapeString(s)
}

// Sender shows notifications with notify-send. Clicking one opens PageUrl.
type Sender struct {
	Bin     string
	PageUrl string
	Logger  *slog.Logger
}

// Send does not wait: notify-send blocks until the notification is clicked or
// closed, so each one runs on its own goroutine.
func (s Sender) Send(n Notification) {
	go func() {
		out, err := exec.Command(s.Bin, "--app-name=elly", "--urgency=normal",
			"--action=default=Open elly", escape(n.Title), escape(n.Body)).Output()
		if err != nil {
			s.Logger.Warn("could not send notification", slog.Any("error", err))
			return
		}
		if strings.TrimSpace(string(out)) == "default" {
			if err := exec.Command("xdg-open", s.PageUrl).Start(); err != nil {
				s.Logger.Warn("could not open elly page", slog.Any("error", err))
			}
		}
	}()
}
