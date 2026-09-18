package types

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"
)

// ViewPr must contain everything needed to order/compare them against other PRs,
// since ViewPr is also what we store.
// Degradation is something elly knows it cannot do, named so a consumer does
// not have to infer it from missing data. The motivating case: a fine-grained
// PAT without "Checks: read" makes github answer 200 with the rollup state
// intact and every check context null, which is indistinguishable from a PR
// with nothing failing unless elly says so.
type Degradation struct {
	// Kind is a stable identifier, e.g. "checks_unreadable".
	Kind string
	// What is wrong, in one sentence, for a human.
	Message string
	// Remedy is the action that fixes it, or "" when there is nothing to do.
	Remedy string
	// Seen is when elly last hit it.
	Seen time.Time
}

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
	RereviewFrom             []string // reviewers of my PR who reviewed before my latest change and were not re-requested
	Buried                   bool
	// ChecksState is github's own statusCheckRollup verdict for the head
	// commit: SUCCESS, FAILURE, PENDING, EXPECTED, or "" when the PR has no
	// checks at all. It is deliberately stored separately from ReviewStatus,
	// which says only whether an approving review exists and nothing about CI:
	// a PR can be REVIEW_REQUIRED and bright red at the same time, and reading
	// the review field alone is how a red PR gets sent to a reviewer.
	ChecksState string
	// ChecksFailing are the names of the failing checks, and ChecksComplete is
	// false when more exist than were read - so a consumer can say "at least
	// N" instead of under-reporting.
	ChecksFailing  []string
	ChecksComplete bool
	// Mergeable is github's verdict on whether the branch still merges into
	// its base: MERGEABLE, CONFLICTING, or UNKNOWN. UNKNOWN is not a rare
	// error case - github computes the merge lazily, so the first poll after a
	// push routinely returns it, and it resolves on a later one. Treat it as
	// "not known yet", never as "no conflict": a PR whose base moved under it
	// is the author's to fix and every check on it can still be green, which
	// is exactly how a conflicting PR gets reported as ready.
	Mergeable string
	// MergeStateStatus is the richer state behind the merge button: CLEAN,
	// DIRTY (conflict), BEHIND (base moved, needs an update), BLOCKED (a
	// required review or check is missing), UNSTABLE, DRAFT, HAS_HOOKS or
	// UNKNOWN. Kept for context and for BEHIND, which nothing else here
	// reports; the conflict decision itself belongs to Mergeable, which is the
	// field github defines for it.
	MergeStateStatus string
	RawJsonResponse  json.RawMessage
}

// HasConflict reports whether the PR no longer merges into its base. It is
// deliberately false for UNKNOWN: github answers UNKNOWN while it is still
// computing the merge, and reporting that as a conflict would cry wolf on
// every PR for the seconds after a push. Callers that need to distinguish
// "known to be fine" from "not computed yet" should read Mergeable directly.
func (pr ViewPr) HasConflict() bool {
	return pr.Mergeable == "CONFLICTING"
}

// ChecksRed reports whether CI is failing on the head commit. Callers should
// prefer this over inspecting ChecksFailing, which is a display detail and can
// be a lower bound; the rollup state is authoritative.
func (pr ViewPr) ChecksRed() bool {
	return pr.ChecksState == "FAILURE" || pr.ChecksState == "ERROR"
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
