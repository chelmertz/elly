package main

import (
	"math/rand"
	"testing"
)

func Test_WhenReviewThreadIsEmpty_WillNotRequireAction(t *testing.T) {
	constructedButEmpty := actionableThreads(prSearchResultGraphQl{ReviewThreads: struct {
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

	actuallyEmpty := actionableThreads(prSearchResultGraphQl{}, "currentUser")

	if actuallyEmpty != 0 {
		t.Fatalf("expected 0 actionable threads for an empty pr, got %d", actuallyEmpty)
	}
}

func commentBy(username string) prReviewThreadCommentGraphQl {
	return prReviewThreadCommentGraphQl{Author: struct{ Login string }{Login: username}}
}

func Fuzz_WhenReviewThreadsExist_WillCountUnresponded(f *testing.F) {
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

		actionableThreads := actionableThreads(threads, myUsername)

		if numberOfComments == 0 {
			if actionableThreads != 0 {
				t.Errorf("got %d unresponded threads, expected 0, since there are no comments", actionableThreads)
			}
			// no need to check anything further, skip the rest to avoid "if
			// len(comments)>0" checks all over
			t.Skip("numberOfComments == 0")
		}

		if myPr && lastCommentIsMine {
			if actionableThreads != 0 {
				t.Errorf("got %d unresponded threads, expected 0", actionableThreads)
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
