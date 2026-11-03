package github

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/chelmertz/elly/anonymise"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"time"
)

func Test_WhenHavingUnansweredComments_WillCountTowardsWaiting(t *testing.T) {
	_, got := actionableThreads(prWithThread("author", threadOf(
		prReviewThreadCommentGraphQl{Author: struct{ Login string }{Login: "currentUser"}},
	)), "currentUser")

	if wanted := 1; wanted != got {
		t.Fatalf("expected %d waiting threads, got %d", wanted, got)
	}
}

func commentBy(username string) prReviewThreadCommentGraphQl {
	return prReviewThreadCommentGraphQl{Author: struct{ Login string }{Login: username}}
}

// threadOf mimics what the aliased query returns for a thread holding the
// given comments: only the first and the last one.
func threadOf(comments ...prReviewThreadCommentGraphQl) prReviewThreadGraphQl {
	var t prReviewThreadGraphQl
	if len(comments) > 0 {
		t.FirstComment.Nodes = comments[:1]
		t.LastComment.Nodes = comments[len(comments)-1:]
	}
	return t
}

func prWithThread(author string, thread prReviewThreadGraphQl) prSearchResultGraphQl {
	return prSearchResultGraphQl{
		Author: struct{ Login string }{Login: author},
		ReviewThreads: prReviewThreadConnectionGraphQl{
			Edges: []struct{ Node prReviewThreadGraphQl }{{Node: thread}},
		},
	}
}

func HangingFuzz_WhenReviewThreadsExist_WillCountUnresponded(f *testing.F) {
	myUsername := "itsMe"
	othersUsername := "otherUser"

	f.Add(true, true, false, uint(3), int64(0))
	f.Add(false, true, false, uint(3), int64(0))

	f.Fuzz(func(t *testing.T, myPr bool, firstCommentIsMine bool, lastCommentIsMine bool, numberOfComments uint, randomSeed int64) {
		// TODO we're looking at a single review thread per test, there should be a test that looks at multiple threads too (but that test could skip the calculation of points, and only care about the sum of the counts)
		allComments := make([]prReviewThreadCommentGraphQl, 0)
		if numberOfComments > 0 {
			if firstCommentIsMine {
				allComments = append(allComments, commentBy(myUsername))
			} else {
				allComments = append(allComments, commentBy(othersUsername))
			}
			random := rand.New(rand.NewSource(randomSeed))
			// "-2" because we compensate for firstCommentIsMine and lastCommentIsMine
			for i := 0; uint(i) < numberOfComments-2; i++ {
				// we're hoping to cover most real cases, there are monologues, back and forths, unresponded comments, etc.
				if random.Int()%2 == 0 {
					allComments = append(allComments, commentBy(myUsername))
				} else {
					allComments = append(allComments, commentBy(othersUsername))
				}
			}
			if numberOfComments > 1 {
				if lastCommentIsMine {
					allComments = append(allComments, commentBy(myUsername))
				} else {
					allComments = append(allComments, commentBy(othersUsername))
				}
			}
		}

		var prAuthor string
		if myPr {
			prAuthor = myUsername
		} else {
			prAuthor = othersUsername
		}
		threads := prWithThread(prAuthor, threadOf(allComments...))

		actionableThreads, _ := actionableThreads(threads, myUsername)

		if numberOfComments == 0 {
			if actionableThreads != 0 {
				t.Errorf("got %d actionable threads, expected 0, since there are no comments", actionableThreads)
			}
			// no need to check anything further, skip the rest to avoid "if
			// len(comments)>0" checks all over
			t.Skip("numberOfComments == 0")
		}

		if myPr && lastCommentIsMine {
			if actionableThreads != 0 {
				t.Errorf("got %d actionable threads, expected 0", actionableThreads)
			}
		}

		myComments := countCommentsByUser(threads, myUsername)
		if myPr && !lastCommentIsMine {
			if actionableThreads == 0 {
				t.Error("someone is waiting on my comment but we got 0")
			}
		}

		if !myPr && len(myComments) == 0 {
			if actionableThreads != 0 {
				t.Errorf("someone else's PR, and I wasn't part of the thread, should have gotten 0 but got %d", actionableThreads)
			}
		}
	})
}

func countCommentsByUser(pr prSearchResultGraphQl, username string) map[int]struct{} {
	myCommentIndexes := make(map[int]struct{})
	for i, t := range pr.ReviewThreads.Edges {
		if t.Node.IsCollapsed || t.Node.IsOutdated || t.Node.IsResolved {
			continue
		}

		for _, c := range append(t.Node.FirstComment.Nodes, t.Node.LastComment.Nodes...) {
			if c.Author.Login == username {
				myCommentIndexes[i] = struct{}{}
			}
		}
	}

	return myCommentIndexes
}

func fixedResponseServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func Test_QueryGithub_WrapsSentinelErrorsSoCallersCanMatchThem(t *testing.T) {
	noRetryPause(t)
	cases := []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"504 with json body is a server error", 504, `{"message":"We couldn't respond to your request in time."}`, ErrGithubServer},
		{"502 with html body is a server error", 502, `<html>Bad gateway</html>`, ErrGithubServer},
		{"404 is a client error", 404, `{"message":"Not Found"}`, ErrClient},
		{"200 with unparseable body is a client error", 200, `not json`, ErrClient},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := fixedResponseServer(t, tc.status, tc.body)
			_, _, err := QueryGithub(srv.URL, "token", "user", slog.New(slog.NewTextHandler(io.Discard, nil)))
			if !errors.Is(err, tc.want) {
				t.Fatalf("errors.Is(%v, %v) = false", err, tc.want)
			}
		})
	}
}

func Test_QueryGithub_CountsServerErrorsAsServerErrors(t *testing.T) {
	noRetryPause(t)
	srv := fixedResponseServer(t, 504, `{"message":"timeout"}`)
	before := counterValue(t, githubRequestsTotal.WithLabelValues("server_error"))
	_, _, _ = QueryGithub(srv.URL, "token", "user", slog.New(slog.NewTextHandler(io.Discard, nil)))
	after := counterValue(t, githubRequestsTotal.WithLabelValues("server_error"))
	if after != before+1 {
		t.Fatalf("server_error counter went from %v to %v, expected +1", before, after)
	}
}

func counterValue(t *testing.T, c prometheus.Counter) float64 {
	t.Helper()
	var m dto.Metric
	if err := c.Write(&m); err != nil {
		t.Fatal(err)
	}
	return m.GetCounter().GetValue()
}

// A PR with more review threads than the search query's first page must have
// the remaining pages fetched per PR before counting, or the newest threads
// (which come last) are ignored: seen 2026-09-08 on a PR with 80 threads,
// six of them unresolved with the reviewer's last word, reported as zero.
func Test_QueryGithub_PaginatesReviewThreadsPerPR(t *testing.T) {
	var pageRequests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Query     string
			Variables map[string]any
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
		}
		q := payload.Query
		switch {
		case strings.Contains(q, "search(type: ISSUE"):
			// first page: one resolved thread, more to come
			_, _ = w.Write([]byte(`{"data":{"search":{"edges":[{"node":{
				"id":"PR_1","url":"https://github.com/o/r/pull/1","title":"t","author":{"login":"me"},
				"updatedAt":"2026-09-08T10:00:00Z","repository":{"url":"https://github.com/o/r","name":"r","owner":{"login":"o"}},
				"reviewThreads":{"totalCount":2,"pageInfo":{"hasNextPage":true,"endCursor":"c1"},"edges":[
					{"node":{"isResolved":true,"firstComment":{"nodes":[{"author":{"login":"other"}}]},"lastComment":{"nodes":[{"author":{"login":"me"},"reactions":{"edges":[]}}]}}}
				]}
			}}]}}}`))
		case strings.Contains(q, "reviewThreads(first: "+reviewThreadPageSize) && payload.Variables["id"] == "PR_1" && payload.Variables["after"] == "c1":
			pageRequests++
			// second page: the unresolved thread where the reviewer has the last word
			_, _ = w.Write([]byte(`{"data":{"node":{"reviewThreads":{"totalCount":2,"pageInfo":{"hasNextPage":false,"endCursor":"c2"},"edges":[
				{"node":{"isResolved":false,"firstComment":{"nodes":[{"author":{"login":"other"}}]},"lastComment":{"nodes":[{"author":{"login":"other"},"reactions":{"edges":[]}}]}}}
			]}}}}`))
		case strings.Contains(q, "nodes(ids:"):
			// mergeability is a batched follow-up for our own PRs
			_, _ = w.Write([]byte(`{"data":{"nodes":[
				{"id":"PR_1","mergeable":"CONFLICTING","mergeStateStatus":"DIRTY"}
			]}}`))
		default:
			t.Errorf("unexpected query: %s", q)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(srv.Close)

	logger, logs := bufferLogger()
	prs, _, err := queryGithub(srv.URL, "token", "me", logger)
	if err != nil {
		t.Fatal(err)
	}
	if len(prs) != 1 || prs[0].ThreadsActionable != 1 {
		t.Fatalf("expected the second page's unresolved thread to count as actionable, got %+v", prs)
	}
	if strings.Contains(logs.String(), "could not fetch all review threads") {
		t.Errorf("a page that was fetched was logged as missing:\n%s", logs)
	}
	if pageRequests != 1 {
		t.Fatalf("expected exactly one follow-up page request, got %d", pageRequests)
	}
}

// The follow-up query must fetch the same thread fields as the search query,
// or the two pages are counted by different rules.
func Test_ReviewThreadPageQuery_SharesThreadFields(t *testing.T) {
	q := queryReviewThreadsPage("PR_1", "c1").Query
	for _, want := range []string{"node(id: $id)", "after: $after", "firstComment: comments(first: 1)", "lastComment: comments(last: 1)", "isResolved", "pageInfo"} {
		if !strings.Contains(q, want) {
			t.Errorf("page query lacks %q", want)
		}
	}
	if !strings.Contains(querySearchPrs(qualifierInvolves, "me").Query, "hasNextPage") {
		t.Error("search query lacks pageInfo, so no follow-up can ever happen")
	}
}

func Test_ReviewedBy(t *testing.T) {
	var pr prSearchResultGraphQl
	for _, login := range []string{"adam", "me", "dependabot[bot]", "adam", "eve"} {
		var e struct {
			Node struct {
				Author                  struct{ Login string }
				Url, State, SubmittedAt string
			}
		}
		e.Node.Author.Login = login
		pr.Reviews.Edges = append(pr.Reviews.Edges, e)
	}
	if got := strings.Join(reviewedBy(pr, "me"), ","); got != "adam,eve" {
		t.Fatalf("reviewedBy = %q, want adam,eve", got)
	}
}

func Test_RereviewFrom(t *testing.T) {
	type review struct{ login, state, at string }
	build := func(author string, draft bool, myLastCommit string, requested []string, reviews []review, myThreadReplyAt string) prSearchResultGraphQl {
		var pr prSearchResultGraphQl
		pr.Author.Login = author
		pr.IsDraft = draft
		if myLastCommit != "" {
			pr.Commits.Nodes = append(pr.Commits.Nodes, prCommitNodeGraphQl{})
			pr.Commits.Nodes[0].Commit.Author.Date = myLastCommit
		}
		for _, r := range requested {
			var n struct {
				RequestedReviewer requestedReviewerGraphQl
			}
			n.RequestedReviewer.Login = r
			pr.ReviewRequests.Nodes = append(pr.ReviewRequests.Nodes, n)
		}
		for _, r := range reviews {
			var e struct {
				Node struct {
					Author                  struct{ Login string }
					Url, State, SubmittedAt string
				}
			}
			e.Node.Author.Login, e.Node.State, e.Node.SubmittedAt = r.login, r.state, r.at
			pr.Reviews.Edges = append(pr.Reviews.Edges, e)
		}
		if myThreadReplyAt != "" {
			c := commentBy("me")
			c.CreatedAt = myThreadReplyAt
			pr.ReviewThreads.Edges = append(pr.ReviewThreads.Edges, struct{ Node prReviewThreadGraphQl }{Node: threadOf(commentBy("adam"), c)})
		}
		return pr
	}
	before, after := "2026-09-01T10:00:00Z", "2026-09-02T10:00:00Z"
	cases := []struct {
		name string
		pr   prSearchResultGraphQl
		open int
		want []string
	}{
		{"reviewer commented, I pushed after", build("me", false, after, nil, []review{{"adam", "COMMENTED", before}}, ""), 0, []string{"adam"}},
		{"changes requested, I replied in a thread after", build("me", false, "", nil, []review{{"adam", "CHANGES_REQUESTED", before}}, after), 0, []string{"adam"}},
		{"reviewer's latest review is newer than my push", build("me", false, before, nil, []review{{"adam", "COMMENTED", after}}, ""), 0, []string{}},
		{"already re-requested", build("me", false, after, []string{"adam"}, []review{{"adam", "COMMENTED", before}}, ""), 0, []string{}},
		{"approved, nothing to ask", build("me", false, after, nil, []review{{"adam", "APPROVED", before}}, ""), 0, []string{}},
		{"approved after commenting: latest review wins", build("me", false, after, nil, []review{{"adam", "COMMENTED", before}, {"adam", "APPROVED", before}}, ""), 0, []string{}},
		{"I still owe answers", build("me", false, after, nil, []review{{"adam", "COMMENTED", before}}, ""), 2, []string{}},
		{"not my PR", build("adam", false, after, nil, []review{{"me", "COMMENTED", before}}, ""), 0, []string{}},
		{"draft", build("me", true, after, nil, []review{{"adam", "COMMENTED", before}}, ""), 0, []string{}},
		{"bots and my own reviews are ignored", build("me", false, after, nil, []review{{"github-actions", "COMMENTED", before}, {"dep[bot]", "COMMENTED", before}, {"me", "COMMENTED", before}}, ""), 0, []string{}},
		{"two reviewers, one re-requested", build("me", false, after, []string{"eve"}, []review{{"adam", "COMMENTED", before}, {"eve", "CHANGES_REQUESTED", before}}, ""), 0, []string{"adam"}},
	}
	for _, c := range cases {
		got := rereviewFrom(c.pr, "me", c.open)
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

// Reactions are the "acknowledged" mechanism: a thumbs-up from us closes an
// otherwise actionable thread, and someone else's reaction to our last word
// reopens it. Neither branch was covered.
func Test_ReactionsDecideAcknowledgement(t *testing.T) {
	reactBy := func(logins ...string) prReviewThreadCommentReactionGraphQl {
		var r prReviewThreadCommentReactionGraphQl
		for _, l := range logins {
			var e struct {
				Node struct {
					Content string
					User    struct{ Login string }
				}
			}
			e.Node.Content, e.Node.User.Login = "THUMBS_UP", l
			r.Edges = append(r.Edges, e)
		}
		return r
	}
	// someone else has the last word on my PR, but I reacted to it
	theirs := commentBy("adam")
	theirs.Reactions = reactBy("me")
	if a, _ := actionableThreads(prWithThread("me", threadOf(commentBy("adam"), theirs)), "me"); a != 0 {
		t.Errorf("my reaction must acknowledge their last comment, got %d actionable", a)
	}
	// I have the last word on my PR and someone else reacted to it
	mine := commentBy("me")
	mine.Reactions = reactBy("adam")
	if a, _ := actionableThreads(prWithThread("me", threadOf(commentBy("me"), mine)), "me"); a != 1 {
		t.Errorf("their reaction to my last comment must be actionable, got %d", a)
	}
	// I started a thread on someone else's PR and they answered last
	if a, _ := actionableThreads(prWithThread("adam", threadOf(commentBy("me"), commentBy("adam"))), "me"); a != 1 {
		t.Errorf("my own question answered by the author must be actionable, got %d", a)
	}
	// resolved and collapsed threads never count; an outdated thread does not
	// count here because someone else started it
	for _, mark := range []func(*prReviewThreadGraphQl){
		func(th *prReviewThreadGraphQl) { th.IsResolved = true },
		func(th *prReviewThreadGraphQl) { th.IsOutdated = true },
		func(th *prReviewThreadGraphQl) { th.IsCollapsed = true },
	} {
		th := threadOf(commentBy("adam"), commentBy("adam"))
		mark(&th)
		if a, w := actionableThreads(prWithThread("me", th), "me"); a != 0 || w != 0 {
			t.Errorf("thread someone else started counted: %d actionable, %d waiting", a, w)
		}
	}
}

// The usual answer to a review comment is a push that moves the commented
// lines, then "done" in the thread. The push marks the thread outdated, and
// the reply is still owed a look from whoever started it.
func Test_OutdatedThreadIStartedAndTheyAnswered_IsActionable(t *testing.T) {
	th := threadOf(commentBy("me"), commentBy("author"))
	th.IsOutdated = true
	if a, w := actionableThreads(prWithThread("author", th), "me"); a != 1 || w != 0 {
		t.Errorf("outdated thread answered by the author: got %d actionable, %d waiting, want 1, 0", a, w)
	}

	resolved := th
	resolved.IsResolved = true
	if a, _ := actionableThreads(prWithThread("author", resolved), "me"); a != 0 {
		t.Errorf("resolved outdated thread must not count, got %d", a)
	}

	reacted := threadOf(commentBy("me"), commentBy("author"))
	reacted.IsOutdated = true
	reacted.LastComment.Nodes[0].Reactions.Edges = append(reacted.LastComment.Nodes[0].Reactions.Edges, struct {
		Node struct {
			Content string
			User    struct{ Login string }
		}
	}{})
	reacted.LastComment.Nodes[0].Reactions.Edges[0].Node.User.Login = "me"
	if a, _ := actionableThreads(prWithThread("author", reacted), "me"); a != 0 {
		t.Errorf("my reaction acknowledges the reply on an outdated thread too, got %d", a)
	}

	mineLast := threadOf(commentBy("me"), commentBy("author"), commentBy("me"))
	mineLast.IsOutdated = true
	if a, _ := actionableThreads(prWithThread("author", mineLast), "me"); a != 0 {
		t.Errorf("I have the last word on an outdated thread, got %d actionable", a)
	}
}

// The last PR commenter drives points and p-launcher's "who spoke last";
// CI bots must not take that slot from a human.
func Test_QueryGithub_IgnoresBotsAsLastCommenter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"search":{"edges":[{"node":{
			"id":"PR_1","url":"https://github.com/o/r/pull/1","title":"t","author":{"login":"me"},
			"updatedAt":"2026-09-08T10:00:00Z","repository":{"url":"https://github.com/o/r","name":"r","owner":{"login":"o"}},
			"comments":{"edges":[
				{"node":{"author":{"login":"adam"},"updatedAt":"2026-09-08T09:00:00Z"}},
				{"node":{"author":{"login":"github-actions"},"updatedAt":"2026-09-08T09:30:00Z"}},
				{"node":{"author":{"login":"vercel"},"updatedAt":"2026-09-08T09:45:00Z"}}
			]},
			"reviewThreads":{"totalCount":0,"pageInfo":{"hasNextPage":false},"edges":[]}
		}}]}}}`))
	}))
	t.Cleanup(srv.Close)
	prs, _, err := queryGithub(srv.URL, "token", "me", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if len(prs) != 1 || prs[0].LastPrCommenter != "adam" {
		t.Fatalf("expected the human as last commenter, got %+v", prs)
	}
}

// An approval that github leaves out of reviewDecision is picked up from the
// reviews list, but only when nothing stronger is set: a PR with changes
// requested must not be reported as approved.
func Test_QueryGithub_ApprovalFromReviewsOnlyWhenNoDecision(t *testing.T) {
	pr := func(decision string) string {
		return `{"data":{"search":{"edges":[{"node":{
			"id":"PR_1","url":"https://github.com/o/r/pull/1","title":"t","author":{"login":"me"},
			"updatedAt":"2026-09-08T10:00:00Z","repository":{"url":"https://github.com/o/r","name":"r","owner":{"login":"o"}},
			"reviewDecision":` + decision + `,
			"reviews":{"edges":[{"node":{"author":{"login":"adam"},"state":"APPROVED","submittedAt":"2026-09-07T10:00:00Z"}}]},
			"reviewThreads":{"totalCount":0,"pageInfo":{"hasNextPage":false},"edges":[]}
		}}]}}}`
	}
	for _, c := range []struct{ decision, want string }{
		{`""`, "APPROVED"},
		{`"CHANGES_REQUESTED"`, "CHANGES_REQUESTED"},
		{`"REVIEW_REQUIRED"`, "REVIEW_REQUIRED"},
	} {
		srv := fixedResponseServer(t, 200, pr(c.decision))
		prs, _, err := queryGithub(srv.URL, "token", "me", slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err != nil {
			t.Fatal(err)
		}
		if len(prs) != 1 || prs[0].ReviewStatus != c.want {
			t.Errorf("reviewDecision %s: want %s, got %+v", c.decision, c.want, prs)
		}
	}
}

// A failing follow-up page must not fail the whole poll: the PR keeps the
// threads the search returned, and every other PR is still stored.
func Test_QueryGithub_ThreadPageFailureKeepsThePoll(t *testing.T) {
	noRetryPause(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct{ Query string }
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if strings.Contains(payload.Query, "search(type: ISSUE") {
			_, _ = w.Write([]byte(`{"data":{"search":{"edges":[{"node":{
				"id":"PR_1","url":"https://github.com/o/r/pull/1","title":"t","author":{"login":"me"},
				"updatedAt":"2026-09-08T10:00:00Z","repository":{"url":"https://github.com/o/r","name":"r","owner":{"login":"o"}},
				"reviewThreads":{"totalCount":2,"pageInfo":{"hasNextPage":true,"endCursor":"c1"},"edges":[
					{"node":{"isResolved":false,"firstComment":{"nodes":[{"author":{"login":"adam"}}]},"lastComment":{"nodes":[{"author":{"login":"adam"},"reactions":{"edges":[]}}]}}}
				]}
			}}]}}}`))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	logger, logs := bufferLogger()
	prs, _, err := queryGithub(srv.URL, "token", "me", logger)
	if err != nil {
		t.Fatalf("a failed thread page must not fail the poll: %v", err)
	}
	if !strings.Contains(logs.String(), "could not fetch all review threads") {
		t.Errorf("the lower-bound count must be logged:\n%s", logs)
	}
	if len(prs) != 1 || prs[0].ThreadsActionable != 1 {
		t.Fatalf("expected the first page's thread to still count, got %+v", prs)
	}
}

func Test_GhExpiration(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	got, ok := ghExpiration("2026-12-24 09:00:00 +0100", logger)
	if !ok || !got.Equal(time.Date(2026, 12, 24, 8, 0, 0, 0, time.UTC)) {
		t.Fatalf("got %v %v", got, ok)
	}
	if _, ok := ghExpiration("tomorrow", logger); ok {
		t.Fatal("unparseable expiration must not be reported as parsed")
	}
}

// A cancelled check blocks a merge exactly like a failing one. Reading it as
// "not failing" is how a red PR gets reported as ready for review, which is
// the mistake the checks columns exist to prevent.
func TestCheckContextFailed(t *testing.T) {
	for _, tc := range []struct {
		name string
		ctx  checkContext
		want bool
	}{
		{"action failed", checkContext{Name: "test", Conclusion: "FAILURE"}, true},
		{"action cancelled", checkContext{Name: "test", Conclusion: "CANCELLED"}, true},
		{"action timed out", checkContext{Name: "test", Conclusion: "TIMED_OUT"}, true},
		{"action passed", checkContext{Name: "test", Conclusion: "SUCCESS"}, false},
		{"action still running", checkContext{Name: "test", Status: "IN_PROGRESS"}, false},
		{"external reporter failed", checkContext{Context: "buildkite/webapp", State: "FAILURE"}, true},
		{"external reporter passed", checkContext{Context: "buildkite/webapp", State: "SUCCESS"}, false},
		{"lowercase from a status context", checkContext{Context: "ci", State: "failure"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.ctx.failed(); got != tc.want {
				t.Errorf("failed() = %v, want %v", got, tc.want)
			}
		})
	}
}

// The two shapes name themselves differently; a check with neither must still
// be reportable rather than appearing as an empty string in a list of failures.
func TestCheckContextLabel(t *testing.T) {
	if got := (checkContext{Name: "build (infra/keycloak)"}).label(); got != "build (infra/keycloak)" {
		t.Errorf("CheckRun label = %q", got)
	}
	if got := (checkContext{Context: "buildkite/webapp"}).label(); got != "buildkite/webapp" {
		t.Errorf("StatusContext label = %q", got)
	}
	if got := (checkContext{}).label(); got == "" {
		t.Error("a nameless check must still have a label")
	}
}

// The names of the failing checks come from a follow-up query. When this was
// first deployed the rollup state stored correctly while every name came back
// empty, so the parse gets a canned response of the shape github actually
// returns, including both context shapes and a second page.
func Test_fetchFailingChecks(t *testing.T) {
	page1 := `{"data":{"node":{"commits":{"nodes":[{"commit":{"statusCheckRollup":{"contexts":{
		"totalCount": 4,
		"pageInfo": {"hasNextPage": true, "endCursor": "CURSOR1"},
		"nodes": [
			{"__typename":"CheckRun","name":"scan (infra/apidocs)","conclusion":"FAILURE","status":"COMPLETED"},
			{"__typename":"CheckRun","name":"build","conclusion":"SUCCESS","status":"COMPLETED"}
		]}}}}]}}}}`
	page2 := `{"data":{"node":{"commits":{"nodes":[{"commit":{"statusCheckRollup":{"contexts":{
		"totalCount": 4,
		"pageInfo": {"hasNextPage": false, "endCursor": ""},
		"nodes": [
			{"__typename":"StatusContext","context":"buildkite/webapp","state":"FAILURE"},
			{"__typename":"StatusContext","context":"buildkite/webapp/pipeline","state":"SUCCESS"}
		]}}}}]}}}}`

	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			_, _ = w.Write([]byte(page1))
			return
		}
		_, _ = w.Write([]byte(page2))
	}))
	defer srv.Close()

	failing, complete, _ := api{baseURL: srv.URL, token: "token", logger: slog.New(slog.NewTextHandler(io.Discard, nil))}.fetchFailingChecks("PR_node", "https://github.com/o/r/pull/1")
	if !complete {
		t.Error("both pages were served, so the list must be complete")
	}
	want := []string{"scan (infra/apidocs)", "buildkite/webapp"}
	if len(failing) != len(want) {
		t.Fatalf("failing = %v, want %v", failing, want)
	}
	for i := range want {
		if failing[i] != want[i] {
			t.Errorf("failing[%d] = %q, want %q", i, failing[i], want[i])
		}
	}
}

// A fine-grained PAT without "Checks: read" answers 200 with the rollup state
// intact, every context null, and the refusal only in the top-level errors
// array. That must never render as "red, 0 failing checks".
func Test_fetchFailingChecks_RefusedContextsAreNotAnEmptyList(t *testing.T) {
	body := `{"data":{"node":{"commits":{"nodes":[{"commit":{"statusCheckRollup":{"contexts":{
		"totalCount": 138,
		"pageInfo": {"hasNextPage": false, "endCursor": ""},
		"nodes": [null, null]}}}}]}}},
		"errors":[{"type":"FORBIDDEN","message":"Resource not accessible by personal access token"}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	failing, complete, _ := api{baseURL: srv.URL, token: "token", logger: slog.New(slog.NewTextHandler(io.Discard, nil))}.fetchFailingChecks("PR_node", "https://github.com/o/r/pull/1")
	if len(failing) != 0 {
		t.Errorf("no names are readable, got %v", failing)
	}
	if complete {
		t.Error("a refused response must report as incomplete, or a red PR reads as having no failing checks")
	}
}

// noRetryPause makes the 5xx retry immediate for tests. Without it the suite
// pays the real 1s+3s pause on every server-error case, which took the package
// from 0.02s to 16s.
func noRetryPause(t *testing.T) {
	t.Helper()
	original := serverErrorBackoff
	serverErrorBackoff = []time.Duration{0}
	t.Cleanup(func() { serverErrorBackoff = original })
}

// A 502 is github giving up on a query it answers fine seconds later, so it is
// retried before the caller ever hears about it.
func Test_graphqlRequest_RetriesServerErrors(t *testing.T) {
	noRetryPause(t)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("<html>Bad gateway</html>"))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"ok":true}}`))
	}))
	defer srv.Close()

	body, err := graphqlRequest(srv.URL, graphqlQuery{Query: "{}"}, "token", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("two 502s then a 200 must succeed, got %v", err)
	}
	if calls != 3 {
		t.Errorf("expected 3 attempts, got %d", calls)
	}
	if !strings.Contains(string(body), "ok") {
		t.Errorf("body = %s", body)
	}
}

// Retries are bounded: a server that is really down must still surface as an
// error rather than looping.
func Test_graphqlRequest_GivesUpAfterRetries(t *testing.T) {
	noRetryPause(t)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	_, err := graphqlRequest(srv.URL, graphqlQuery{Query: "{}"}, "token", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if !errors.Is(err, ErrGithubServer) {
		t.Fatalf("err = %v, want ErrGithubServer", err)
	}
	if calls != serverErrorRetries+1 {
		t.Errorf("expected %d attempts, got %d", serverErrorRetries+1, calls)
	}
}

// A rate limit is an answer, not a hiccup: repeating it helps nobody and
// spends the little quota that is left.
func Test_graphqlRequest_DoesNotRetryNonServerErrors(t *testing.T) {
	noRetryPause(t)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"errors":[{"type":"FORBIDDEN","message":"nope"}]}`))
	}))
	defer srv.Close()

	_, _ = graphqlRequest(srv.URL, graphqlQuery{Query: "{}"}, "token", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if calls != 1 {
		t.Errorf("a 4xx must be asked once, got %d attempts", calls)
	}
}

// The refusal has to come back as a named degradation, because the whole point
// is that elly says what it cannot do instead of leaving it to be derived.
func Test_fetchFailingChecks_ReportsTheMissingPermission(t *testing.T) {
	noRetryPause(t)
	body := `{"data":{"node":{"commits":{"nodes":[{"commit":{"statusCheckRollup":{"contexts":{
		"totalCount": 138, "pageInfo": {"hasNextPage": false, "endCursor": ""},
		"nodes": [null]}}}}]}}},
		"errors":[{"type":"FORBIDDEN","message":"Resource not accessible by personal access token"}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	var logs bytes.Buffer
	_, complete, degraded := api{baseURL: srv.URL, token: "token", logger: slog.New(slog.NewTextHandler(&logs, nil))}.fetchFailingChecks("PR_node", "https://github.com/o/r/pull/1")
	// Every red PR hits this on every poll with a fine-grained token, so a
	// WARN here is per-poll noise about something no token setting can fix.
	if strings.Contains(logs.String(), "level=WARN") {
		t.Errorf("a refused check list is the token type, not a fault, and must not warn:\n%s", logs.String())
	}
	if complete {
		t.Error("a refused list is not a complete one")
	}
	if degraded == nil {
		t.Fatal("the refusal must be reported, not only logged")
	}
	if degraded.Kind != "checks_unreadable" {
		t.Errorf("kind = %q", degraded.Kind)
	}
	// The remedy used to say "grant this token the Checks permission", which no
	// fine-grained token can be given: github offers Checks to Github Apps only,
	// and the permission does not appear in the token UI at all. Advice that
	// cannot be followed is worse than none, so assert against the literal
	// wording rather than against a constant this file could rename with it.
	if strings.Contains(degraded.Remedy, "Grant this token") {
		t.Errorf("the remedy must not ask for a permission github will not issue, got %q", degraded.Remedy)
	}
	for _, want := range []string{"Github Apps", "classic", "repo"} {
		if !strings.Contains(degraded.Remedy, want) {
			t.Errorf("the remedy must mention %q, got %q", want, degraded.Remedy)
		}
	}
}

// A review requested from a team used to land as an empty string: the query
// unwrapped "... on User" only, so a Team node parsed into a zero-valued
// struct and every PR in a team-reviewing organisation reported [""] . The
// empty name then read as "waiting on review from " with nobody named.
func Test_QueryGithub_TeamReviewersAreNamed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"search":{"edges":[{"node":{
			"id":"PR_1","url":"https://github.com/o/r/pull/1","title":"t","author":{"login":"me"},
			"updatedAt":"2026-09-08T10:00:00Z","repository":{"url":"https://github.com/o/r","name":"r","owner":{"login":"o"}},
			"reviewRequests":{"nodes":[
				{"requestedReviewer":{"login":"adam"}},
				{"requestedReviewer":{"name":"backend"}},
				{"requestedReviewer":{}}
			]},
			"comments":{"edges":[]},
			"reviewThreads":{"totalCount":0,"pageInfo":{"hasNextPage":false},"edges":[]}
		}}]}}}`))
	}))
	t.Cleanup(srv.Close)
	prs, _, err := queryGithub(srv.URL, "token", "me", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if len(prs) != 1 {
		t.Fatalf("expected one pr, got %d", len(prs))
	}
	got := prs[0].ReviewRequestedFromUsers
	want := []string{"adam", "@backend"}
	if len(got) != len(want) {
		t.Fatalf("reviewers = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("reviewer %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// github's "involves:" is author OR assignee OR mentions OR commenter, and
// review requests are none of those. A PR someone asked me to review and that
// I have not spoken on was invisible until a second search was added.
func Test_QueryGithub_IncludesReviewRequestedPrs(t *testing.T) {
	pr := func(n, author string) string {
		return fmt.Sprintf(`{"node":{
			"id":"PR_%s","url":"https://github.com/o/r/pull/%s","title":"t%s","author":{"login":%q},
			"updatedAt":"2026-09-08T10:00:00Z","repository":{"url":"https://github.com/o/r","name":"r","owner":{"login":"o"}},
			"comments":{"edges":[]},
			"reviewThreads":{"totalCount":0,"pageInfo":{"hasNextPage":false},"edges":[]}
		}}`, n, n, n, author)
	}
	var qualifiers []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "nodes(ids:") {
			// the mergeability follow-up, not one of the two searches
			_, _ = fmt.Fprint(w, `{"data":{"nodes":[{"id":"PR_1","mergeable":"MERGEABLE","mergeStateStatus":"CLEAN"}]}}`)
			return
		}
		if strings.Contains(string(body), "review-requested:") {
			qualifiers = append(qualifiers, "review-requested")
			// pull/1 is in both searches and must survive as one row.
			_, _ = fmt.Fprintf(w, `{"data":{"search":{"edges":[%s,%s]}}}`, pr("1", "me"), pr("2", "adam"))
			return
		}
		qualifiers = append(qualifiers, "involves")
		_, _ = fmt.Fprintf(w, `{"data":{"search":{"edges":[%s]}}}`, pr("1", "me"))
	}))
	t.Cleanup(srv.Close)

	prs, degradations, err := queryGithub(srv.URL, "token", "me", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if len(qualifiers) != 2 {
		t.Fatalf("expected both searches, got %v", qualifiers)
	}
	if len(degradations) != 0 {
		t.Errorf("both searches answered, so nothing is degraded: %+v", degradations)
	}
	var urls []string
	for _, p := range prs {
		urls = append(urls, p.Url)
	}
	if len(urls) != 2 {
		t.Fatalf("the PR in both searches must appear once, got %q", urls)
	}
	if !slices.Contains(urls, "https://github.com/o/r/pull/2") {
		t.Errorf("the review-requested PR is missing, got %q", urls)
	}
}

// A failed second search must not take the whole poll with it, and must not
// pass silently either: the list is then missing review requests and says so.
func Test_QueryGithub_ReviewRequestedFailureIsDegradedNotFatal(t *testing.T) {
	noRetryPause(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "review-requested:") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"search":{"edges":[{"node":{
			"id":"PR_1","url":"https://github.com/o/r/pull/1","title":"t","author":{"login":"me"},
			"updatedAt":"2026-09-08T10:00:00Z","repository":{"url":"https://github.com/o/r","name":"r","owner":{"login":"o"}},
			"comments":{"edges":[]},
			"reviewThreads":{"totalCount":0,"pageInfo":{"hasNextPage":false},"edges":[]}
		}}]}}}`))
	}))
	t.Cleanup(srv.Close)

	prs, degradations, err := queryGithub(srv.URL, "token", "me", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("the first search answered, so the poll stands: %v", err)
	}
	if len(prs) != 1 {
		t.Errorf("expected the involves: result to survive, got %d", len(prs))
	}
	var kinds []string
	for _, d := range degradations {
		kinds = append(kinds, d.Kind)
	}
	if !slices.Contains(kinds, "review_requests_unreadable") {
		t.Errorf("a missing second search must be reported, got %q", kinds)
	}
}

// Backoff lives in the caller and keys off the error, so a rate limit hit by
// the second search has to propagate. Degrading instead would leave the poll
// running at full cadence against a github that is already refusing it.
func Test_QueryGithub_RateLimitOnReviewRequestedStillBacksOff(t *testing.T) {
	noRetryPause(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "review-requested:") {
			w.Header().Set("Retry-After", "60")
			_, _ = w.Write([]byte(`{"errors":[{"type":"RATE_LIMITED","message":"API rate limit exceeded"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"search":{"edges":[]}}}`))
	}))
	t.Cleanup(srv.Close)

	_, _, err := queryGithub(srv.URL, "token", "me", slog.New(slog.NewTextHandler(io.Discard, nil)))
	var rl *ErrRateLimited
	if !errors.As(err, &rl) {
		t.Fatalf("a rate limit must reach the caller, got %v", err)
	}
}

// UsernameFromPat is what a pasted PAT is checked with: its status code
// decides whether elly drops the token, backs off or reports a fault.
func Test_UsernameFromPat_ClassifiesResponses(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cases := []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"unauthorized drops the token", 401, "", ErrInvalidToken},
		{"forbidden drops the token", 403, "", ErrInvalidToken},
		{"other client error", 404, "", ErrClient},
		{"server error", 502, "", ErrGithubServer},
		{"500 is a server error", 500, "", ErrGithubServer},
		{"499 is a client error", 499, "", ErrClient},
		{"graphql errors", 200, `{"errors":[{"type":"X","message":"nope"}]}`, ErrClient},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := fixedResponseServer(t, c.status, c.body)
			if _, _, err := UsernameFromPat(srv.URL, "tok", logger); !errors.Is(err, c.want) {
				t.Errorf("got %v, want %v", err, c.want)
			}
		})
	}
	t.Run("unparseable body", func(t *testing.T) {
		srv := fixedResponseServer(t, 200, "not json")
		if _, _, err := UsernameFromPat(srv.URL, "tok", logger); err == nil {
			t.Error("a garbled response read as a valid token")
		}
	})
}

func Test_UsernameFromPat_ReadsLoginAndExpiry(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		w.Header().Set("Github-Authentication-Token-Expiration", "2026-12-24 09:00:00 +0100")
		_, _ = w.Write([]byte(`{"data":{"viewer":{"login":"octocat"}}}`))
	}))
	t.Cleanup(srv.Close)
	login, expires, err := UsernameFromPat(srv.URL, "tok", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil || login != "octocat" || !expires.Equal(time.Date(2026, 12, 24, 8, 0, 0, 0, time.UTC)) {
		t.Errorf("got %q %v %v", login, expires, err)
	}
	if auth != "bearer tok" {
		t.Errorf("Authorization %q", auth)
	}
}

func bufferLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewTextHandler(&buf, nil)), &buf
}

// The expiry warning is the only notice a user gets before a PAT stops
// working: it fires inside ten days and not before.
func Test_GhExpiration_WarnsOnlyInsideTenDays(t *testing.T) {
	layout := "2006-01-02 15:04:05 -0700"
	for _, c := range []struct {
		in   time.Duration
		warn bool
	}{{5 * 24 * time.Hour, true}, {9 * 24 * time.Hour, true}, {11 * 24 * time.Hour, false}, {30 * 24 * time.Hour, false}} {
		logger, logs := bufferLogger()
		if _, ok := ghExpiration(time.Now().Add(c.in).Format(layout), logger); !ok {
			t.Fatalf("%v: not parsed", c.in)
		}
		if got := strings.Contains(logs.String(), "github token expires soon"); got != c.warn {
			t.Errorf("expiring in %v: warned = %v, want %v", c.in, got, c.warn)
		}
		// a day short of five is four whole days left
		if want := "days_left=4\n"; c.in == 5*24*time.Hour && !strings.Contains(logs.String(), want) {
			t.Errorf("expiring in %v: logs lack %q:\n%s", c.in, want, logs)
		}
	}
}

// A response without the expiry header is a token that never expires, not
// an expiry that could not be parsed.
func Test_Request_NoExpiryHeaderLogsNoParseError(t *testing.T) {
	srv := fixedResponseServer(t, 200, `{"data":{}}`)
	logger, logs := bufferLogger()
	if _, err := graphqlRequest(srv.URL, graphqlQuery{Query: "{}"}, "token", logger); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logs.String(), "could not parse github token expiration") {
		t.Errorf("a missing header was logged as unparseable:\n%s", logs)
	}
}

// Each retry is logged with its own number, counting from 1.
func Test_graphqlRequest_LogsEachRetryByNumber(t *testing.T) {
	noRetryPause(t)
	srv := fixedResponseServer(t, http.StatusBadGateway, "")
	logger, logs := bufferLogger()
	_, _ = graphqlRequest(srv.URL, graphqlQuery{Query: "{}"}, "token", logger)
	for _, want := range []string{"attempt=1 ", "attempt=2 "} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("logs lack %q:\n%s", want, logs)
		}
	}
	if strings.Contains(logs.String(), "attempt=3 ") {
		t.Errorf("two retries logged as three:\n%s", logs)
	}
}

// The status code decides the class at both ends of each range: a 500 is
// github's fault even with a json body, a 400 is the caller's even when the
// body parses.
func Test_Request_ClassifiesBoundaryStatusCodes(t *testing.T) {
	noRetryPause(t)
	for _, c := range []struct {
		status int
		want   error
	}{{500, ErrGithubServer}, {400, ErrClient}} {
		srv := fixedResponseServer(t, c.status, `{}`)
		if _, err := graphqlRequest(srv.URL, graphqlQuery{Query: "{}"}, "token", slog.New(slog.DiscardHandler)); !errors.Is(err, c.want) {
			t.Errorf("%d: got %v, want %v", c.status, err, c.want)
		}
	}
	srv := fixedResponseServer(t, 400, "")
	if _, _, err := UsernameFromPat(srv.URL, "tok", slog.New(slog.DiscardHandler)); !errors.Is(err, ErrClient) {
		t.Errorf("UsernameFromPat 400: got %v, want ErrClient", err)
	}
}

// Either rate-limit header alone says when to come back.
func Test_Request_RateLimitReadsEachHeader(t *testing.T) {
	rateLimited := func(header, value string) *ErrRateLimited {
		t.Helper()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set(header, value)
			_, _ = w.Write([]byte(`{"errors":[{"type":"RATE_LIMITED","message":"slow down"}]}`))
		}))
		t.Cleanup(srv.Close)
		_, err := graphqlRequest(srv.URL, graphqlQuery{Query: "{}"}, "token", slog.New(slog.DiscardHandler))
		var rl *ErrRateLimited
		if !errors.As(err, &rl) {
			t.Fatalf("%s: got %v, want a rate limit", header, err)
		}
		return rl
	}
	reset := time.Now().Add(time.Hour).Truncate(time.Second)
	if got := rateLimited("x-ratelimit-reset", fmt.Sprint(reset.Unix())); !got.UnblockedAt.Equal(reset) {
		t.Errorf("x-ratelimit-reset: unblocked at %v, want %v", got.UnblockedAt, reset)
	}
	before := time.Now()
	got := rateLimited("retry-after", "60")
	if d := got.UnblockedAt.Sub(before); d < 59*time.Second || d > 61*time.Second {
		t.Errorf("retry-after 60: unblocked %v from now, want 60s", d)
	}
}

func searchPrNode(extra string) string {
	return `{"id":"PR_1","url":"https://github.com/o/r/pull/1","title":"t","author":{"login":"me"},
		"updatedAt":"2026-09-08T10:00:00Z","repository":{"url":"https://github.com/o/r","name":"r","owner":{"login":"o"}},
		"reviewThreads":{"totalCount":0,"pageInfo":{"hasNextPage":false},"edges":[]}` + extra + `}`
}

// A red PR whose failing checks were all named is complete; two different
// refusals in one poll come back once each, in kind order.
func Test_QueryGithub_RedPrNamesAndDegradationOrder(t *testing.T) {
	noRetryPause(t)
	red := `,"commits":{"nodes":[{"commit":{"statusCheckRollup":{"state":"FAILURE"}}}]}`
	for _, c := range []struct {
		name     string
		checks   string
		complete bool
		kinds    string
	}{
		{"named", `{"data":{"node":{"commits":{"nodes":[{"commit":{"statusCheckRollup":{"contexts":{"totalCount":1,"pageInfo":{"hasNextPage":false},
			"nodes":[{"__typename":"CheckRun","name":"build","conclusion":"FAILURE","status":"COMPLETED"}]}}}}]}}}}`, true, "review_requests_unreadable"},
		{"refused", `{"data":{"node":{"commits":{"nodes":[{"commit":{"statusCheckRollup":{"contexts":{"totalCount":1,"pageInfo":{"hasNextPage":false},
			"nodes":[null]}}}}]}}},"errors":[{"type":"FORBIDDEN","message":"no"}]}`, false, "checks_unreadable,review_requests_unreadable"},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				switch q := string(body); {
				case strings.Contains(q, "review-requested:"):
					w.WriteHeader(http.StatusInternalServerError)
				case strings.Contains(q, "contexts(first"):
					_, _ = w.Write([]byte(c.checks))
				case strings.Contains(q, "nodes(ids:"):
					_, _ = w.Write([]byte(`{"data":{"nodes":[]}}`))
				default:
					_, _ = w.Write([]byte(`{"data":{"search":{"edges":[{"node":` + searchPrNode(red) + `}]}}}`))
				}
			}))
			t.Cleanup(srv.Close)
			prs, degradations, err := queryGithub(srv.URL, "token", "me", slog.New(slog.DiscardHandler))
			if err != nil {
				t.Fatal(err)
			}
			if len(prs) != 1 || prs[0].ChecksComplete != c.complete {
				t.Fatalf("ChecksComplete: got %+v, want %v", prs, c.complete)
			}
			var kinds []string
			for _, d := range degradations {
				kinds = append(kinds, d.Kind)
			}
			if strings.Join(kinds, ",") != c.kinds {
				t.Errorf("degradations %v, want %s", kinds, c.kinds)
			}
		})
	}
}

// Someone else's PR comment is not the user answering, so it does not hand
// the PR back to the reviewer.
func Test_RereviewFrom_OnlyTheUsersCommentsCount(t *testing.T) {
	var pr prSearchResultGraphQl
	pr.Author.Login = "me"
	pr.Commits.Nodes = append(pr.Commits.Nodes, prCommitNodeGraphQl{})
	pr.Commits.Nodes[0].Commit.Author.Date = "2026-09-01T09:00:00Z"
	var review struct {
		Node struct {
			Author                  struct{ Login string }
			Url, State, SubmittedAt string
		}
	}
	review.Node.Author.Login, review.Node.State, review.Node.SubmittedAt = "adam", "COMMENTED", "2026-09-01T10:00:00Z"
	pr.Reviews.Edges = append(pr.Reviews.Edges, review)
	comment := func(login, at string) {
		var c struct {
			Node struct {
				UpdatedAt string
				Author    struct{ Login string }
				Url       string
			}
		}
		c.Node.Author.Login, c.Node.UpdatedAt = login, at
		pr.Comments.Edges = append(pr.Comments.Edges, c)
	}
	comment("eve", "2026-09-02T10:00:00Z")
	if got := rereviewFrom(pr, "me", 0); len(got) != 0 {
		t.Errorf("eve's comment asked adam to re-review: %v", got)
	}
	comment("me", "2026-09-02T11:00:00Z")
	if got := rereviewFrom(pr, "me", 0); strings.Join(got, ",") != "adam" {
		t.Errorf("the user's own reply: got %v, want [adam]", got)
	}
}

// A full batch is one request, not a full one and an empty one; a failed
// batch is logged with its size.
func Test_fetchMergeability_BatchesExactly(t *testing.T) {
	noRetryPause(t)
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	ids := map[string]string{}
	for i := range mergeabilityBatchSize {
		ids[fmt.Sprintf("https://github.com/o/r/pull/%d", i)] = fmt.Sprintf("PR_%03d", i)
	}
	api{baseURL: srv.URL, logger: slog.New(slog.DiscardHandler)}.fetchMergeability(ids)
	if requests != serverErrorRetries+1 {
		t.Errorf("one batch of %d took %d requests, want %d", mergeabilityBatchSize, requests, serverErrorRetries+1)
	}

	ids["https://github.com/o/r/pull/extra"] = "PR_999"
	logger, logs := bufferLogger()
	api{baseURL: srv.URL, logger: logger}.fetchMergeability(ids)
	for _, want := range []string{fmt.Sprintf("batch_size=%d ", mergeabilityBatchSize), "batch_size=1 "} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("logs lack %q:\n%s", want, logs)
		}
	}
}

// Both follow-up loops are bounded: a server that always claims another page
// gets exactly the page budget and no more.
func Test_FollowUpPagesAreBounded(t *testing.T) {
	requests := 0
	more := func(body string) *httptest.Server {
		requests = 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests++
			// stop a runaway loop after one page over the budget
			_, _ = w.Write([]byte(strings.ReplaceAll(body, "HAS_NEXT", fmt.Sprint(requests <= 10))))
		}))
		t.Cleanup(srv.Close)
		return srv
	}
	srv := more(`{"data":{"node":{"commits":{"nodes":[{"commit":{"statusCheckRollup":{"contexts":{"totalCount":9999,
		"pageInfo":{"hasNextPage":HAS_NEXT,"endCursor":"c"},"nodes":[{"__typename":"CheckRun","name":"x","conclusion":"FAILURE","status":"COMPLETED"}]}}}}]}}}}`)
	_, complete, _ := api{baseURL: srv.URL, logger: slog.New(slog.DiscardHandler)}.fetchFailingChecks("PR_1", "u")
	if requests != maxCheckContextPages || complete {
		t.Errorf("checks: %d requests, complete %v; want %d, false", requests, complete, maxCheckContextPages)
	}

	srv = more(`{"data":{"node":{"reviewThreads":{"totalCount":9999,"pageInfo":{"hasNextPage":HAS_NEXT,"endCursor":"c"},"edges":[]}}}}`)
	var pr prSearchResultGraphQl
	pr.ReviewThreads.PageInfo.HasNextPage = true
	if err := (api{baseURL: srv.URL, logger: slog.New(slog.DiscardHandler)}).fetchRemainingReviewThreads(&pr); err != nil {
		t.Fatal(err)
	}
	if requests != maxReviewThreadPages {
		t.Errorf("review threads: %d requests, want %d", requests, maxReviewThreadPages)
	}
}

// The search returned page 1, so the first follow-up is page 2.
func Test_fetchRemainingReviewThreads_NamesTheFailedPage(t *testing.T) {
	srv := fixedResponseServer(t, 200, `not json`)
	var pr prSearchResultGraphQl
	pr.ReviewThreads.PageInfo.HasNextPage = true
	err := api{baseURL: srv.URL, logger: slog.New(slog.DiscardHandler)}.fetchRemainingReviewThreads(&pr)
	if err == nil || !strings.Contains(err.Error(), "review threads page 2 ") {
		t.Errorf("got %v, want an error naming page 2", err)
	}
}

// Each part of a github URL is mapped, never dropped.
func Test_RecordingURL_MapsEveryPart(t *testing.T) {
	rules := anonymise.Rules{Paths: map[string]anonymise.Rule{"url": recordingURL(recordingLogin("me"))}}
	out, err := anonymise.Anonymise(rules, []byte(`{"url":"https://github.com/acme/rocket/pull/7#issuecomment-123"}`))
	if err != nil {
		t.Fatal(err)
	}
	var got struct{ Url string }
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if want := "https://github.com/owner1/repo1/pull/1#issuecomment-1"; got.Url != want {
		t.Errorf("got %q, want %q", got.Url, want)
	}
}

// GitHub answers a query that timed out on its side with HTTP 200 and
// {"data":null,"errors":[...]}. That is no answer: read as an empty search it
// replaced the stored list with nothing and counted as a successful poll.
func Test_QueryGithub_AnAnswerWithoutDataIsAFailedPoll(t *testing.T) {
	noRetryPause(t)
	for _, body := range []string{
		`{"data":null,"errors":[{"message":"Something went wrong while executing your query. This may be the result of a timeout."}]}`,
		`{"errors":[{"type":"INTERNAL","message":"boom"}]}`,
		`null`,
		`{}`,
	} {
		srv := fixedResponseServer(t, 200, body)
		prs, _, err := QueryGithub(srv.URL, "token", "user", slog.New(slog.DiscardHandler))
		if err == nil {
			t.Errorf("%s: read as a successful poll with %d PRs", body, len(prs))
		}
	}
}
