package github

import (
	"errors"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func Test_WhenReviewThreadIsEmpty_WillNotRequireAction(t *testing.T) {
	constructedButEmpty, _ := actionableThreads(prSearchResultGraphQl{ReviewThreads: struct {
		Edges []struct{ Node prReviewThreadGraphQl }
	}{
		Edges: []struct{ Node prReviewThreadGraphQl }{{Node: prReviewThreadGraphQl{Comments: struct {
			Nodes []prReviewThreadCommentGraphQl
		}{}}}},
	},
	}, "currentUser")

	if constructedButEmpty != 0 {
		t.Fatalf("expected 0 actionable threads on an empty struct, got %d", constructedButEmpty)
	}

	actuallyEmpty, _ := actionableThreads(prSearchResultGraphQl{}, "currentUser")

	if actuallyEmpty != 0 {
		t.Fatalf("expected 0 actionable threads for an empty pr, got %d", actuallyEmpty)
	}
}

func Test_WhenHavingUnansweredComments_WillCountTowardsWaiting(t *testing.T) {
	_, got := actionableThreads(prSearchResultGraphQl{ReviewThreads: struct {
		Edges []struct{ Node prReviewThreadGraphQl }
	}{
		Edges: []struct{ Node prReviewThreadGraphQl }{{Node: prReviewThreadGraphQl{Comments: struct {
			Nodes []prReviewThreadCommentGraphQl
		}{
			[]prReviewThreadCommentGraphQl{{Author: struct{ Login string }{Login: "currentUser"}, Body: "a question"}},
		}}}},
	},
	}, "currentUser")

	if wanted := 1; wanted != got {
		t.Fatalf("expected %d waiting threads, got %d", wanted, got)
	}
}

func commentBy(username string) prReviewThreadCommentGraphQl {
	return prReviewThreadCommentGraphQl{Author: struct{ Login string }{Login: username}}
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
		threads := prSearchResultGraphQl{
			Author: struct{ Login string }{Login: prAuthor},
			ReviewThreads: struct {
				Edges []struct{ Node prReviewThreadGraphQl }
			}{
				Edges: []struct{ Node prReviewThreadGraphQl }{{Node: prReviewThreadGraphQl{Comments: struct {
					Nodes []prReviewThreadCommentGraphQl
				}{
					allComments,
				}}}},
			},
		}

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

		for _, c := range t.Node.Comments.Nodes {
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
