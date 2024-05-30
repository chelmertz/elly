package types

import (
	"encoding/base64"
	"fmt"
	"time"
)

// ViewPr must contain everything needed to order/compare them against other PRs,
// since ViewPr is also what we store.
type ViewPr struct {
	ReviewStatus             string
	Url                      string
	Title                    string
	Author                   string
	RepoName                 string
	RepoOwner                string
	RepoUrl                  string
	IsDraft                  bool
	LastUpdated              time.Time
	LastPrCommenter          string
	ThreadsActionable        int
	ThreadsWaiting           int
	Additions                int
	Deletions                int
	ReviewRequestedFromUsers []string
	Buried                   bool
}

func (pr ViewPr) Id() string {
	return base64.StdEncoding.EncodeToString([]byte(pr.Url))
}

// ToggleBuryUrl() introduces the concept of base64-encoded-PR-URL as an "ID", in the
// sense of a REST API's resource. This feels cleaner than having to escape
// things, or constructing an ID with a owner/repo/pr# or such. If we want to
// support something else than Github later, an URL is still a good ID.
func (pr ViewPr) ToggleBuryUrl() string {
	if pr.Buried {
		return fmt.Sprintf("/api/v0/prs/%s/unbury", pr.Id())
	} else {
		return fmt.Sprintf("/api/v0/prs/%s/bury", pr.Id())
	}
}

type RefreshAction string

const (
	RefreshUpstart RefreshAction = "upstart"
	RefreshStop    RefreshAction = "stop"
	RefreshTick    RefreshAction = "tick"
	RefreshManual  RefreshAction = "manual"
)
