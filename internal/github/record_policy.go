package github

import (
	"errors"
	"regexp"
	"strconv"
	"strings"

	"github.com/chelmertz/elly/anonymise"
)

// This file is elly's anonymisation policy for a recorded poll: what every
// path of the four queries' requests and responses holds, which enum values
// github may answer there, and how github's URLs and search strings are
// rebuilt. The anonymise package supplies only the mechanism.

// recordingLogin maps a login: the recording user becomes "me", the
// commenters elly ignores are kept because the code branches on them, a bot
// keeps its "[bot]", and anyone else is userN.
func recordingLogin(self string) anonymise.Rule {
	fixed := map[string]string{}
	for _, l := range ignoredLastPrCommenters {
		fixed[l] = l
	}
	if self != "" {
		fixed[self] = "me"
	}
	return anonymise.Rule{Class: anonymise.Counter, Kind: "login", Prefix: "user", Fixed: fixed, Suffixes: []string{"[bot]"}}
}

var (
	recordingOwner = anonymise.Rule{Class: anonymise.Counter, Kind: "owner", Prefix: "owner"}
	recordingRepo  = anonymise.Rule{Class: anonymise.Counter, Kind: "repo", Prefix: "repo"}
	// fetchMergeability sorts node ids before batching them, so mapped ids
	// must sort as the real ones did, and be zero-padded to sort as strings.
	recordingNodeID = anonymise.Rule{Class: anonymise.Counter, Kind: "node id", Prefix: "ID_", Format: "%06d", Sorted: true}
	recordingSHA    = anonymise.Rule{Class: anonymise.Counter, Kind: "sha", Format: "%040d", Pattern: regexp.MustCompile(`^[0-9a-fA-F]{40}$`)}
	recordingCursor = anonymise.Rule{Class: anonymise.Counter, Kind: "cursor", Prefix: "cursor"}
)

// githubURL is a repository, a PR, or a comment, review or review-thread
// comment on a PR. The numbers are renumbered per recording: they are global
// and monotonic on github, so they would date it.
var githubURL = regexp.MustCompile(`^https://github\.com/([^/?#]+)/([^/?#]+)(?:/pull/([0-9]+)(?:#(issuecomment-|pullrequestreview-|discussion_r)([0-9]+))?)?$`)

func recordingURL(login anonymise.Rule) anonymise.Rule {
	owner := `(?:` + anonymise.CounterPattern(recordingOwner) + `|` + anonymise.CounterPattern(login) + `)`
	return anonymise.Rule{
		Class: anonymise.Func, Kind: "url",
		Rewrite: func(m *anonymise.Mapper, raw string) (string, error) {
			p := githubURL.FindStringSubmatch(raw)
			if p == nil {
				return "", errors.New("url value is not a known github.com form")
			}
			// a user's own repository is owned by a login
			ownerRule := recordingOwner
			if _, fixed := login.Fixed[p[1]]; fixed {
				ownerRule = login
			}
			o, err := m.Apply(ownerRule, p[1])
			if err != nil {
				return "", err
			}
			r, err := m.Apply(recordingRepo, p[2])
			if err != nil {
				return "", err
			}
			out := "https://github.com/" + o + "/" + r
			if p[3] != "" {
				// the same number in another repository is another PR
				out += "/pull/" + strconv.Itoa(m.Number("pull", p[1]+"/"+p[2]+"#"+p[3]))
			}
			if p[4] != "" {
				out += "#" + p[4] + strconv.Itoa(m.Number(p[4], p[5]))
			}
			return out, nil
		},
		Form: regexp.MustCompile(`^https://github\.com/` + owner + `/` + anonymise.CounterPattern(recordingRepo) +
			`(?:/pull/[0-9]+(?:#(?:issuecomment-|pullrequestreview-|discussion_r)[0-9]+)?)?$`),
	}
}

// recordingSearch maps a search string token by token. Only the login varies;
// any qualifier querySearchPrs does not write is refused.
func recordingSearch(login anonymise.Rule) anonymise.Rule {
	qualifiers := map[string]anonymise.Rule{
		"state":                  anonymise.EnumOf("open"),
		"type":                   anonymise.EnumOf("pr"),
		"archived":               anonymise.EnumOf("false"),
		qualifierInvolves:        login,
		qualifierReviewRequested: login,
	}
	return anonymise.Rule{
		Class: anonymise.Func, Kind: "search",
		Rewrite: func(m *anonymise.Mapper, raw string) (string, error) {
			tokens := strings.Split(raw, " ")
			for i, tok := range tokens {
				k, v, _ := strings.Cut(tok, ":")
				rule, ok := qualifiers[k]
				if !ok {
					return "", errors.New("search value has a qualifier the poll never writes")
				}
				mapped, err := m.Apply(rule, v)
				if err != nil {
					return "", err
				}
				tokens[i] = k + ":" + mapped
			}
			return strings.Join(tokens, " "), nil
		},
		Form: regexp.MustCompile(`^state:open (?:` + qualifierInvolves + `|` + qualifierReviewRequested + `):` +
			anonymise.CounterPattern(login) + ` type:pr archived:false$`),
	}
}

// The enum values github answers at each path. An answer outside them refuses
// the recording, so a new value is looked at before it is published.
var (
	reviewDecisions  = anonymise.EnumOf("APPROVED", "CHANGES_REQUESTED", "REVIEW_REQUIRED")
	reviewStates     = anonymise.EnumOf("APPROVED", "CHANGES_REQUESTED", "COMMENTED", "DISMISSED", "PENDING")
	rollupStates     = anonymise.EnumOf("SUCCESS", "FAILURE", "ERROR", "EXPECTED", "PENDING")
	checkConclusions = anonymise.EnumOf("SUCCESS", "FAILURE", "NEUTRAL", "CANCELLED", "SKIPPED", "STALE",
		"TIMED_OUT", "ACTION_REQUIRED", "STARTUP_FAILURE")
	checkStatuses = anonymise.EnumOf("QUEUED", "IN_PROGRESS", "COMPLETED", "WAITING", "PENDING", "REQUESTED")
	contextTypes  = anonymise.EnumOf("CheckRun", "StatusContext")
	reactions     = anonymise.EnumOf("THUMBS_UP", "THUMBS_DOWN", "LAUGH", "HOORAY", "CONFUSED", "HEART", "ROCKET", "EYES")
	mergeables    = anonymise.EnumOf("MERGEABLE", "CONFLICTING", "UNKNOWN")
	mergeStates   = anonymise.EnumOf("BEHIND", "BLOCKED", "CLEAN", "DIRTY", "DRAFT", "HAS_HOOKS", "UNKNOWN", "UNSTABLE")
	graphqlErrors = anonymise.EnumOf("FORBIDDEN", "NOT_FOUND")
	keep          = anonymise.As(anonymise.Keep)
	timestamp     = anonymise.As(anonymise.Timestamp)
	redact        = anonymise.As(anonymise.Redact)
)

// publishedRules are the rules a committed recording is checked against. The
// recording user is "me" there, the only login a published file knows.
func publishedRules() anonymise.Rules { return recordingRules("me") }

// recordingRules is the allowlist for a recording of elly's poll by self:
// every path a request or a response of the four queries can hold.
func recordingRules(self string) anonymise.Rules {
	login := recordingLogin(self)
	url := recordingURL(login)
	paths := map[string]anonymise.Rule{
		"pollTime":                             timestamp,
		"exchanges[].status":                   keep,
		"exchanges[].request.query":            anonymise.ConstantOf(queryConstants...),
		"exchanges[].request.variables.search": recordingSearch(login),
		"exchanges[].request.variables.id":     recordingNodeID,
		"exchanges[].request.variables.ids[]":  recordingNodeID,
		"exchanges[].request.variables.after":  recordingCursor,
	}
	add := func(prefix string, fields map[string]anonymise.Rule) {
		for k, v := range fields {
			paths["exchanges[].response."+prefix+k] = v
		}
	}
	threads := map[string]anonymise.Rule{
		"totalCount":                                                         keep,
		"pageInfo.hasNextPage":                                               keep,
		"pageInfo.endCursor":                                                 recordingCursor,
		"edges[].node.isResolved":                                            keep,
		"edges[].node.isOutdated":                                            keep,
		"edges[].node.isCollapsed":                                           keep,
		"edges[].node.firstComment.nodes[].author.login":                     login,
		"edges[].node.lastComment.nodes[].author.login":                      login,
		"edges[].node.lastComment.nodes[].url":                               url,
		"edges[].node.lastComment.nodes[].createdAt":                         timestamp,
		"edges[].node.lastComment.nodes[].reactions.edges[].node.content":    reactions,
		"edges[].node.lastComment.nodes[].reactions.edges[].node.user.login": login,
	}
	pr := map[string]anonymise.Rule{
		"id":      recordingNodeID,
		"title":   redact,
		"url":     url,
		"isDraft": keep,
		"reviewRequests.nodes[].requestedReviewer.login": login,
		"reviewRequests.nodes[].requestedReviewer.name":  redact,
		"repository.url":                                 url,
		"repository.name":                                recordingRepo,
		"repository.owner.login":                         recordingOwner,
		"reviewDecision":                                 reviewDecisions,
		"updatedAt":                                      timestamp,
		"author.login":                                   login,
		"additions":                                      keep,
		"deletions":                                      keep,
		"headRefOid":                                     recordingSHA,
		"comments.edges[].node.updatedAt":                timestamp,
		"comments.edges[].node.author.login":             login,
		"comments.edges[].node.url":                      url,
		"commits.nodes[].commit.author.date":             timestamp,
		"commits.nodes[].commit.author.email":            redact,
		"commits.nodes[].commit.author.name":             redact,
		"commits.nodes[].commit.statusCheckRollup.state": rollupStates,
		"reviews.edges[].node.author.login":              login,
		"reviews.edges[].node.url":                       url,
		"reviews.edges[].node.state":                     reviewStates,
		"reviews.edges[].node.submittedAt":               timestamp,
	}
	for k, v := range threads {
		pr["reviewThreads."+k] = v
	}
	add("data.search.edges[].node.", pr)
	add("data.node.reviewThreads.", threads)
	add("data.node.commits.nodes[].commit.statusCheckRollup.contexts.", map[string]anonymise.Rule{
		"totalCount":           keep,
		"pageInfo.hasNextPage": keep,
		"pageInfo.endCursor":   recordingCursor,
		"nodes[].__typename":   contextTypes,
		"nodes[].name":         redact,
		"nodes[].conclusion":   checkConclusions,
		"nodes[].status":       checkStatuses,
		"nodes[].context":      redact,
		"nodes[].state":        rollupStates,
	})
	add("data.nodes[].", map[string]anonymise.Rule{
		"id":               recordingNodeID,
		"mergeable":        mergeables,
		"mergeStateStatus": mergeStates,
	})
	add("errors[].", map[string]anonymise.Rule{
		"type":               graphqlErrors,
		"message":            redact,
		"path[]":             redact,
		"locations[].line":   keep,
		"locations[].column": keep,
	})
	return anonymise.Rules{
		Paths: paths,
		Vocabulary: []string{
			"https://github.com/", "/pull/", "#issuecomment-", "#pullrequestreview-", "#discussion_r",
			"state:open", qualifierInvolves + ":", qualifierReviewRequested + ":", "type:pr", "archived:false",
		},
	}
}
