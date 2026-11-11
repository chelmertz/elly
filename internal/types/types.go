package types

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
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
	// Owner is the repo owner whose token hit it, "" for the primary token.
	Owner string `json:",omitempty"`
}

// DegradationChecksUnreadable is reported on every poll by a fine-grained
// token, which github never grants the Checks permission. Red or green stays
// correct; only the names of failing Actions jobs are missing.
const DegradationChecksUnreadable = "checks_unreadable"

// TokenLimitation says the degradation follows from the kind of token rather
// than from a fault: another token type is the only remedy, so it is kept out
// of the page and logged once per process, but still reported by the API.
func (d Degradation) TokenLimitation() bool {
	return d.Kind == DegradationChecksUnreadable
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
	ReviewedBy               []string // everyone but me and bots who submitted a review; github drops them from the request list when they do
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
	// HeadRefOid is the SHA of the PR's head commit. A change in it is how a
	// consumer tells "new commits pushed" apart from "new comments on the
	// same commits"; elly itself does not read it.
	HeadRefOid      string
	RawJsonResponse json.RawMessage
	// Annotations are what other local tools say about the PR, unexpired
	// only. They are stored apart from the PR, which is replaced every fetch.
	Annotations []Annotation
}

// Annotation is a note another local tool attaches to a PR: whether the PR
// is handled, needs the user, is stuck on the user, or has work on it
// running, in its own words,
// with links. elly knows
// nothing about the tool beyond its source name.
type Annotation struct {
	Source    string           `json:"source"`
	State     string           `json:"state"`
	Note      string           `json:"note"`
	Links     []AnnotationLink `json:"links"`
	ExpiresAt string           `json:"expiresAt"`
}

type AnnotationLink struct {
	Label string `json:"label"`
	Url   string `json:"url"`
	// Primary makes the link the row's action button in place of its verb:
	// how an add-on offers an action (claude-review's Start). One per
	// annotation.
	Primary bool `json:"primary,omitempty"`
}

const (
	AnnotationHandled  = "handled"
	AnnotationNeedsYou = "needs_you"
	// AnnotationRunning says the tool is working on the PR right now. Like
	// handled, it drops the PR's review-owed reasons: the user handed the PR
	// over. The tool replaces it with another state when it finishes, or it
	// expires.
	AnnotationRunning = "running"
	// AnnotationStuck says nothing will move the PR until the user acts: the
	// tool stopped working on it. It counts like needs_you, and the page
	// marks it apart.
	AnnotationStuck = "stuck"
	// AnnotationInfo only carries links (and a note) to the row: it neither
	// hands the PR over nor asks for the user, so whose turn it is stays as
	// elly scored it.
	AnnotationInfo = "info"
)

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

// Id is the PR's URL in URL-safe base64, for paths: the standard alphabet
// holds "/", which would split the path segment. elly's routes decode both
// alphabets (decodePrId in internal/server), since claude-review sends the
// standard one (annotationURL in its elly.go).
func (pr ViewPr) Id() string {
	return base64.URLEncoding.EncodeToString([]byte(pr.Url))
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

// A property of the type json.RawMessage gets printed as a list of bytes, which
// is hard to read. Change the format when printing this through fmt's %v
func (pr ViewPr) String() string {
	b, err := json.MarshalIndent(pr, "", "  ")
	if err != nil {
		return fmt.Sprintf("error marshaling view pr: %v", err)
	}
	return string(b)
}

// Anchor is the PR's element id on the /next page, derived from its URL alone
// so a desktop notification, which carries only the URL, can link straight to
// the row: https://github.com/Owner/repo.name/pull/7 is pr-owner-repo_2ename-7.
// Path segments are joined by "-"; inside one, a-z and 0-9 stay as they are
// and every other byte becomes "_" and two hex digits. Distinct repos keep
// distinct ids (wid.get, wid-get and wid_get are three repos), GitHub's
// case-insensitive names fold to one, and a hostile URL cannot break out of
// the id or the fragment.
func Anchor(prUrl string) string {
	u, err := url.Parse(prUrl)
	path := prUrl
	if err == nil {
		path = u.Path
	}
	parts := strings.FieldsFunc(path, func(r rune) bool { return r == '/' })
	parts = slices.DeleteFunc(parts, func(s string) bool { return s == "pull" })
	var b strings.Builder
	b.WriteString("pr")
	for _, p := range parts {
		b.WriteByte('-')
		lower := strings.ToLower(p)
		for i := 0; i < len(lower); i++ {
			if c := lower[i]; c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
				b.WriteByte(c)
			} else {
				fmt.Fprintf(&b, "_%02x", c)
			}
		}
	}
	return b.String()
}

// Number is the PR number from its URL, or "" when the URL does not end in
// one.
func Number(prUrl string) string {
	n := prUrl[strings.LastIndexByte(prUrl, '/')+1:]
	if n == "" || strings.Trim(n, "0123456789") != "" {
		return ""
	}
	return n
}

// prUrlPattern is a GitHub PR URL. Owner and repo are GitHub's characters
// only; PrParts also refuses "." and "..", so a link built from them cannot
// step out of its path.
var prUrlPattern = regexp.MustCompile(`^https://github\.com/([A-Za-z0-9._-]+)/([A-Za-z0-9._-]+)/pull/([1-9][0-9]*)$`)

// PrParts takes a GitHub PR URL apart, or reports ok false for anything else.
func PrParts(prUrl string) (owner, repo, number string, ok bool) {
	m := prUrlPattern.FindStringSubmatch(prUrl)
	if m == nil || dotSegment(m[1]) || dotSegment(m[2]) {
		return "", "", "", false
	}
	return m[1], m[2], m[3], true
}

func dotSegment(s string) bool { return s == "." || s == ".." }

// LinkNamesPr reports whether link is about the PR at prUrl and nothing else:
// GitHub's page for it (the URL itself, or a sub-path, query or fragment of
// it), or a claude-review:// link for its owner/repo/number with at most a
// verb after it. A row's one-click verb follows only such a link; any other
// annotation link stays in the panel, under its label.
// fromTag is a claude-review start link's one allowed query, the place it
// was clicked; claude-review's EntrySource reads the same shape.
var fromTag = regexp.MustCompile(`^from=[a-z]{1,16}$`)

func LinkNamesPr(prUrl, link string) bool {
	owner, repo, number, ok := PrParts(prUrl)
	if !ok {
		return false
	}
	u, err := url.Parse(link)
	if err != nil || u.User != nil || u.Opaque != "" {
		return false
	}
	segments := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if slices.ContainsFunc(segments, dotSegment) {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return strings.EqualFold(u.Host, "github.com") && len(segments) >= 4 &&
			strings.EqualFold(segments[0], owner) && strings.EqualFold(segments[1], repo) &&
			segments[2] == "pull" && segments[3] == number
	case "claude-review":
		// The start link may say where it was clicked (?from=elly); a verb
		// link carries no query.
		query := u.RawQuery == "" || (len(segments) == 2 && fromTag.MatchString(u.RawQuery))
		return strings.EqualFold(u.Host, owner) && (len(segments) == 2 || len(segments) == 3) &&
			strings.EqualFold(segments[0], repo) && segments[1] == number && query && u.Fragment == ""
	}
	return false
}
