package types

import (
	"encoding/base64"
	"encoding/json"
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
	RawJsonResponse          json.RawMessage
}

// URL- and filesystem friendly ID of a PR.
func (pr ViewPr) Id() string {
	return base64.StdEncoding.EncodeToString([]byte(pr.Url))
}

// ToggleBuryUrl() introduces the concept of base64-encoded-PR-URL as an "ID", in the
// sense of a REST API's resource. This feels cleaner than having to escape
// things, or constructing an ID with a owner/repo/pr# or such. If we want to
// support something else than Github later, a URL is still a good ID.
func (pr ViewPr) ToggleBuryUrl() string {
	if pr.Buried {
		return fmt.Sprintf("/api/v0/prs/%s/unbury", pr.Id())
	} else {
		return fmt.Sprintf("/api/v0/prs/%s/bury", pr.Id())
	}
}

func (pr ViewPr) GoldenUrl() string {
	return fmt.Sprintf("/api/v0/prs/%s/golden", pr.Id())
}

// A property of the type json.RawMessage gets printed as a list of bytes, which
// is hard to read. Change the format when printing this through fmt's %v
func (pr ViewPr) String() string {
	b, err := json.MarshalIndent(pr, "", "  ")
	if err != nil {
		return fmt.Sprintf("error marshaling view pr: %v", err)
	}
	return string(b)
}

