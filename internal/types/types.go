package types

import "time"

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
	UnrespondedThreads       int
	Additions                int
	Deletions                int
	ReviewRequestedFromUsers []string
}
