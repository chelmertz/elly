package main

import "testing"

func Test_WhenReviewThreadIsEmpty_WillNotRequireAction(t *testing.T) {
	constructedButEmpty := actionableThreads(prSearchResultGraphQl{ReviewThreads: struct {
		Edges []struct{ Node prReviewThreadGraphQl }
	}{
		Edges: []struct{ Node prReviewThreadGraphQl }{struct{ Node prReviewThreadGraphQl }{Node: prReviewThreadGraphQl{Comments: struct {
			Nodes []prReviewThreadCommentGraphQl
		}{}}}},
	},
	}, "currentUser")

	if constructedButEmpty != 0 {
		t.Fatalf("expected 0 actionable threads, got %d", constructedButEmpty)
	}

	actuallyEmpty := actionableThreads(prSearchResultGraphQl{}, "currentUser")

	if actuallyEmpty != 0 {
		t.Fatalf("expected 0 actionable threads, got %d", actuallyEmpty)
	}
}

//func Fuzz_WhenReviewThreadsExist_WillCountUnresponded(f *testing.F) {
//	myUsername := "currentUser"
//	unrespondedCount := actionableThreads(prSearchResultGraphQl{ReviewThreads: struct {
//		Edges []struct{ Node prReviewThreadGraphQl }
//	}{
//		Edges: []struct{ Node prReviewThreadGraphQl }{struct{ Node prReviewThreadGraphQl }{Node: prReviewThreadGraphQl{Comments: struct {
//			Nodes []prReviewThreadCommentGraphQl
//		}{
//			[]prReviewThreadCommentGraphQl{prReviewThreadCommentGraphQl{Author: struct{ Login string }{Login: "currentUser"}}},
//		}}}},
//	},
//	}, "currentUser")
//
//	// unresponded == I can take action on it
//
//	// others pr + myUsername is not part of the thread == 0
//	// if count > 0: myUsername is not last comment
//	// our pr + myUsername is not part of the thread > 0
//	// only myUsername commented == 0
//}
