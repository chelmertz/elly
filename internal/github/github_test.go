package github

import (
	"encoding/json"
	"math/rand"
	"strings"
	"testing"
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
		ReviewThreads: struct {
			Edges []struct{ Node prReviewThreadGraphQl }
		}{
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
