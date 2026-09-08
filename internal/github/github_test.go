package github

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func Test_WhenReviewThreadIsEmpty_WillNotRequireAction(t *testing.T) {
	constructedButEmpty, _ := actionableThreads(prWithThread("author", threadOf()), "currentUser")

	if constructedButEmpty != 0 {
		t.Fatalf("expected 0 actionable threads on an empty struct, got %d", constructedButEmpty)
	}

	actuallyEmpty, _ := actionableThreads(prSearchResultGraphQl{}, "currentUser")

	if actuallyEmpty != 0 {
		t.Fatalf("expected 0 actionable threads for an empty pr, got %d", actuallyEmpty)
	}
}

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

// The github graphql "cost" is the product of every nested first/last
// argument, regardless of how much data comes back. Fetching 30 comments
// with 7 reactions each for 15 threads per PR made a single poll cost 475
// of the 5000 hourly points and run right at github's 10 s query timeout.
// Only the first comment's author and the last comment's reactions are used.
func Test_Query_FetchesOnlyFirstAndLastReviewThreadComment(t *testing.T) {
	q := querySearchPrsInvolvingUser("me")
	for _, want := range []string{"firstComment: comments(first: 1)", "lastComment: comments(last: 1)"} {
		if !strings.Contains(q, want) {
			t.Errorf("query lacks %q", want)
		}
	}
	if strings.Contains(q, "comments(first: 30)") {
		t.Error("query still fetches 30 comments per review thread")
	}
	// nothing reads these, and comment bodies alone were 70% of the response
	for _, unused := range []string{"body", "status {"} {
		if strings.Contains(q, unused) {
			t.Errorf("query fetches %q, which is never read", unused)
		}
	}
	if got := strings.Count(q, "reactions("); got != 1 {
		t.Errorf("expected reactions only on the last review thread comment, found %d reactions connections", got)
	}
}

// Counterpart of the aliases in querySearchPrsInvolvingUser: the response
// keys must land in the struct fields actionableThreads reads.
func Test_AliasedThreadCommentsAreParsed(t *testing.T) {
	body := `{
		"author": {"login": "me"},
		"reviewThreads": {"edges": [{"node": {
			"isResolved": false,
			"firstComment": {"nodes": [{"author": {"login": "me"}}]},
			"lastComment": {"nodes": [{"author": {"login": "other"}, "reactions": {"edges": []}}]}
		}}]}
	}`
	var pr prSearchResultGraphQl
	if err := json.Unmarshal([]byte(body), &pr); err != nil {
		t.Fatal(err)
	}
	actionable, _ := actionableThreads(pr, "me")
	if actionable != 1 {
		t.Fatalf("own PR where someone else has the last word: expected 1 actionable thread, got %d", actionable)
	}
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
			_, err := QueryGithub(srv.URL, "token", "user", slog.New(slog.NewTextHandler(io.Discard, nil)))
			if !errors.Is(err, tc.want) {
				t.Fatalf("errors.Is(%v, %v) = false", err, tc.want)
			}
		})
	}
}

func Test_QueryGithub_CountsServerErrorsAsServerErrors(t *testing.T) {
	srv := fixedResponseServer(t, 504, `{"message":"timeout"}`)
	before := counterValue(t, githubRequestsTotal.WithLabelValues("server_error"))
	_, _ = QueryGithub(srv.URL, "token", "user", slog.New(slog.NewTextHandler(io.Discard, nil)))
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
		var payload struct{ Query string }
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
		case strings.Contains(q, `node(id: "PR_1")`) && strings.Contains(q, `after: "c1"`):
			pageRequests++
			// second page: the unresolved thread where the reviewer has the last word
			_, _ = w.Write([]byte(`{"data":{"node":{"reviewThreads":{"totalCount":2,"pageInfo":{"hasNextPage":false,"endCursor":"c2"},"edges":[
				{"node":{"isResolved":false,"firstComment":{"nodes":[{"author":{"login":"other"}}]},"lastComment":{"nodes":[{"author":{"login":"other"},"reactions":{"edges":[]}}]}}}
			]}}}}`))
		default:
			t.Errorf("unexpected query: %s", q)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(srv.Close)

	prs, err := queryGithub(srv.URL, "token", "me", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if len(prs) != 1 || prs[0].ThreadsActionable != 1 {
		t.Fatalf("expected the second page's unresolved thread to count as actionable, got %+v", prs)
	}
	if pageRequests != 1 {
		t.Fatalf("expected exactly one follow-up page request, got %d", pageRequests)
	}
}

// The follow-up query must fetch the same thread fields as the search query,
// or the two pages are counted by different rules.
func Test_ReviewThreadPageQuery_SharesThreadFields(t *testing.T) {
	q := queryReviewThreadsPage("PR_1", "c1")
	for _, want := range []string{`node(id: "PR_1")`, `after: "c1"`, "firstComment: comments(first: 1)", "lastComment: comments(last: 1)", "isResolved", "pageInfo"} {
		if !strings.Contains(q, want) {
			t.Errorf("page query lacks %q", want)
		}
	}
	if !strings.Contains(querySearchPrsInvolvingUser("me"), "hasNextPage") {
		t.Error("search query lacks pageInfo, so no follow-up can ever happen")
	}
}

func Test_RereviewFrom(t *testing.T) {
	type review struct{ login, state, at string }
	build := func(author string, draft bool, myLastCommit string, requested []string, reviews []review, myThreadReplyAt string) prSearchResultGraphQl {
		var pr prSearchResultGraphQl
		pr.Author.Login = author
		pr.IsDraft = draft
		if myLastCommit != "" {
			pr.Commits.Nodes = append(pr.Commits.Nodes, struct {
				Commit struct {
					Author struct{ Date, Email, Name string }
				}
			}{})
			pr.Commits.Nodes[0].Commit.Author.Date = myLastCommit
		}
		for _, r := range requested {
			var n struct {
				RequestedReviewer struct{ Login string }
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
