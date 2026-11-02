package github

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/chelmertz/elly/anonymise"
)

// Every value here is one that must never be published. The second owner is
// not on the allowlist, so nothing of its PR may be recorded or even asked
// about in a follow-up.
const (
	recToken      = "ghp_SuperSecretToken"
	recSelf       = "Zelfie"
	recOwner      = "Workorgx"
	recOtherOwner = "Privatez"
	recOtherNode  = "PR_otherNodeQQ"
)

func recPr(owner, node, title string) string {
	return fmt.Sprintf(`{"node":{
		"id":%q,"url":"https://github.com/%s/Rocketeer/pull/4711","title":%q,"isDraft":false,
		"author":{"login":%q},"updatedAt":"2026-09-08T10:00:00Z","headRefOid":"0123456789abcdef0123456789abcdef01234567",
		"reviewDecision":"REVIEW_REQUIRED","additions":3,"deletions":1,
		"repository":{"url":"https://github.com/%s/Rocketeer","name":"Rocketeer","owner":{"login":%q}},
		"reviewRequests":{"nodes":[{"requestedReviewer":{"login":"Reviewerkat"}},{"requestedReviewer":{"name":"Backendsquad"}}]},
		"comments":{"edges":[{"node":{"updatedAt":"2026-09-08T09:00:00Z","author":{"login":"Reviewerkat"},"url":"https://github.com/%s/Rocketeer/pull/4711#issuecomment-998877"}}]},
		"commits":{"nodes":[{"commit":{"author":{"date":"2026-09-08T08:00:00Z","email":"zelfie@workorgx.example","name":"Zelfie Person"},"statusCheckRollup":{"state":"FAILURE"}}}]},
		"reviewThreads":{"totalCount":2,"pageInfo":{"hasNextPage":true,"endCursor":"Q3Vyc29yU2VjcmV0"},"edges":[]},
		"reviews":{"edges":[{"node":{"author":{"login":"Reviewerkat"},"url":"https://github.com/%s/Rocketeer/pull/4711#pullrequestreview-5544","state":"COMMENTED","submittedAt":"2026-09-08T09:30:00Z"}}]}
	}}`, node, owner, title, recSelf, owner, owner, owner, owner)
}

type fakeGithub struct {
	t        *testing.T
	mu       sync.Mutex
	requests []string
	answer   func(q graphqlQuery) (int, string)
}

func (f *fakeGithub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var q graphqlQuery
	if err := json.Unmarshal(body, &q); err != nil {
		f.t.Errorf("request is not a graphql body: %v", err)
	}
	f.mu.Lock()
	f.requests = append(f.requests, string(body))
	f.mu.Unlock()
	status, resp := f.answer(q)
	w.WriteHeader(status)
	_, _ = io.WriteString(w, resp)
}

// happyGithub answers one own red PR per owner, a second page of threads,
// refused check contexts (a 200 with errors, which is kept) and mergeability.
func happyGithub(q graphqlQuery) (int, string) {
	switch q.Query {
	case searchPrsQuery:
		if strings.Contains(q.Variables["search"].(string), "review-requested:") {
			return 200, `{"data":{"search":{"edges":[]}}}`
		}
		return 200, `{"data":{"search":{"edges":[` + recPr(recOwner, "PR_workNodeQQ", "Secret launch plan") + `,` + recPr(recOtherOwner, recOtherNode, "Private hobby thing") + `]}}}`
	case reviewThreadsPageQuery:
		return 200, `{"data":{"node":{"reviewThreads":{"totalCount":2,"pageInfo":{"hasNextPage":false,"endCursor":"Q3Vyc29yRW5k"},"edges":[
			{"node":{"isResolved":false,"isOutdated":false,"isCollapsed":false,
			 "firstComment":{"nodes":[{"author":{"login":"Reviewerkat"}}]},
			 "lastComment":{"nodes":[{"author":{"login":"Reviewerkat"},"url":"https://github.com/` + recOwner + `/Rocketeer/pull/4711#discussion_r1234567","createdAt":"2026-09-08T09:40:00Z",
			   "reactions":{"edges":[{"node":{"content":"THUMBS_UP","user":{"login":"dependabot[bot]"}}}]}}]}}}
		]}}}}`
	case failingChecksQuery:
		return 200, `{"data":{"node":{"commits":{"nodes":[{"commit":{"statusCheckRollup":{"contexts":{"totalCount":1,
			"pageInfo":{"hasNextPage":false,"endCursor":"Q3Vyc29yQ2hlY2s="},"nodes":[null]}}}}]}}},
			"errors":[{"type":"FORBIDDEN","path":["node","commits","nodes",0],"locations":[{"line":7,"column":13}],"extensions":{"saml_failure":false,"ip_allow_list_failure":false},"message":"Resource not accessible by Zelfie token"}]}`
	case mergeabilityQuery:
		return 200, `{"data":{"nodes":[{"id":"PR_workNodeQQ","mergeable":"MERGEABLE","mergeStateStatus":"BLOCKED"}]}}`
	}
	return 400, `{"message":"unexpected"}`
}

func record(t *testing.T, answer func(graphqlQuery) (int, string)) ([]byte, error, *fakeGithub, string) {
	t.Helper()
	noRetryPause(t)
	fake := &fakeGithub{t: t, answer: answer}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	var logs bytes.Buffer
	// Debug on purpose: whatever the level, nothing raw may reach a log line.
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	out, err := Record(srv.URL, recToken, recSelf, []string{strings.ToLower(recOwner)}, logger)
	return out, err, fake, logs.String()
}

var recSecrets = []string{
	recToken, recSelf, recOwner, recOtherOwner, recOtherNode, "PR_workNodeQQ", "Rocketeer", "Reviewerkat",
	"Backendsquad", "Secret launch plan", "launch", "Private hobby", "Zelfie Person", "workorgx.example",
	"Q3Vyc29y", "0123456789abcdef", "4711", "998877", "5544", "1234567", "Resource not accessible",
	"dependabot", "bearer",
}

func assertNoSecrets(t *testing.T, where, s string) {
	t.Helper()
	lower := strings.ToLower(s)
	for _, secret := range recSecrets {
		if strings.Contains(lower, strings.ToLower(secret)) {
			t.Errorf("%s holds the raw value %q", where, secret)
		}
	}
}

func Test_Record_PublishesOnlyAnonymisedAllowlistedExchanges(t *testing.T) {
	out, err, fake, logs := record(t, happyGithub)
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	assertNoSecrets(t, "the recording", string(out))
	assertNoSecrets(t, "the log", logs)
	if err := anonymise.CheckForms(publishedRules(), out); err != nil {
		t.Errorf("recording fails the forms check: %v", err)
	}

	var rec recording
	if err := json.Unmarshal(out, &rec); err != nil {
		t.Fatal(err)
	}
	// two searches, one thread page and one checks lookup for the allowlisted
	// PR, and its mergeability
	if len(rec.Exchanges) != 5 {
		t.Fatalf("expected 5 exchanges, got %d", len(rec.Exchanges))
	}
	if rec.PollTime.IsZero() {
		t.Error("the poll time is what the PRs are scored against, it must be recorded")
	}
	for _, r := range fake.requests {
		if strings.Contains(r, recOtherNode) {
			t.Errorf("a PR outside the owner allowlist got a follow-up: %s", r)
		}
	}
	var search struct {
		Data struct {
			Search struct{ Edges []json.RawMessage }
		}
	}
	if err := json.Unmarshal(rec.Exchanges[0].Response, &search); err != nil || len(search.Data.Search.Edges) != 1 {
		t.Errorf("the search must hold the allowlisted PR only, got %d edges (%v)", len(search.Data.Search.Edges), err)
	}
}

// Anything but a plain 200 JSON answer means the poll was not a normal one,
// and a recording of it would be a recording of a fault.
func Test_Record_RefusesAbnormalPolls(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		answer       func(graphqlQuery) (int, string)
	}{
		{"server error, which would be retried", "exchange 3: status 502", func(q graphqlQuery) (int, string) {
			if q.Query == reviewThreadsPageQuery {
				return 502, `{"data":null}`
			}
			return happyGithub(q)
		}},
		{"rate limit", "exchange 5: rate limited", func(q graphqlQuery) (int, string) {
			if q.Query == mergeabilityQuery {
				return 200, `{"errors":[{"type":"RATE_LIMITED","message":"API rate limit exceeded for Zelfie"}]}`
			}
			return happyGithub(q)
		}},
		{"non-JSON body", "exchange 4: the body is not JSON", func(q graphqlQuery) (int, string) {
			if q.Query == failingChecksQuery {
				return 200, "Zelfie is not json"
			}
			return happyGithub(q)
		}},
		{"client error", "exchange 2: status 403", func(q graphqlQuery) (int, string) {
			if strings.Contains(fmt.Sprint(q.Variables["search"]), "review-requested:") {
				return 403, `{"message":"no Zelfie"}`
			}
			return happyGithub(q)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err, fake, logs := record(t, tc.answer)
			if err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("expected the recording to be refused for %q, got %v", tc.reason, err)
			}
			if out != nil {
				t.Error("a refused recording must return nothing")
			}
			assertNoSecrets(t, "the error", err.Error())
			assertNoSecrets(t, "the log", logs)
			for i, r := range fake.requests {
				for _, other := range fake.requests[i+1:] {
					if r == other {
						t.Errorf("a request was repeated, so a retry happened: %s", r)
					}
				}
			}
		})
	}
}

// The search string is rebuilt from what querySearchPrs writes; any other
// qualifier or value is refused rather than published.
func Test_RecordingPolicy_RefusesAnUnexpectedSearch(t *testing.T) {
	for _, search := range []string{
		"state:closed involves:someone type:pr archived:false",
		"state:open org:someorg involves:someone type:pr archived:false",
	} {
		doc, _ := json.Marshal(map[string]any{"exchanges": []any{map[string]any{"request": map[string]any{
			"query": searchPrsQuery, "variables": map[string]any{"search": search}}}}})
		if _, err := anonymise.Anonymise(recordingRules("someone"), doc); err == nil {
			t.Errorf("%q was published", search)
		}
	}
}
