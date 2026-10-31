package notify

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chelmertz/elly/internal/types"
)

const me = "me"

var work = []string{"work"}

func pr(url, owner, author string) types.ViewPr {
	return types.ViewPr{Url: url, RepoOwner: owner, Author: author, Title: "t " + url,
		LastUpdated: time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)}
}

func TestFirstRunIsASilentBaseline(t *testing.T) {
	p := pr("a", "work", me)
	p.ReviewStatus = "APPROVED"

	got, seen := Diff(nil, []types.ViewPr{p}, me, work)

	if len(got) != 0 {
		t.Errorf("first run notified %v", got)
	}
	if len(seen["a"]) == 0 {
		t.Error("first run recorded nothing, so the next fetch would notify everything")
	}
}

func TestOwnPrApprovedNotifiesOnce(t *testing.T) {
	p := pr("a", "work", me)
	_, seen := Diff(map[string][]string{}, []types.ViewPr{p}, me, work)

	p.ReviewStatus = "APPROVED"
	got, seen := Diff(seen, []types.ViewPr{p}, me, work)
	if len(got) != 1 || !strings.Contains(got[0].Body, "approved") {
		t.Fatalf("got %v, want one approval notification", got)
	}

	got, _ = Diff(seen, []types.ViewPr{p}, me, work)
	if len(got) != 0 {
		t.Errorf("approval notified again: %v", got)
	}
}

func TestReasonKindNotTextDecidesRepeats(t *testing.T) {
	p := pr("a", "work", "someone")
	p.ThreadsActionable = 1
	_, seen := Diff(map[string][]string{}, []types.ViewPr{p}, me, work)

	p.ThreadsActionable = 3
	got, _ := Diff(seen, []types.ViewPr{p}, me, work)
	if len(got) != 0 {
		t.Errorf("a changed thread count notified again: %v", got)
	}
}

func TestReasonThatLeavesAndReturnsNotifiesAgain(t *testing.T) {
	p := pr("a", "work", me)
	p.ReviewStatus = "APPROVED"
	_, seen := Diff(map[string][]string{}, []types.ViewPr{p}, me, work)

	p.ReviewStatus = ""
	_, seen = Diff(seen, []types.ViewPr{p}, me, work)
	p.ReviewStatus = "APPROVED"
	got, _ := Diff(seen, []types.ViewPr{p}, me, work)
	if len(got) != 1 {
		t.Errorf("re-approval after a dismissal: got %v, want one", got)
	}
}

func TestReviewRequestedOnSomeoneElsesPr(t *testing.T) {
	p := pr("a", "work", "someone")
	_, seen := Diff(map[string][]string{}, []types.ViewPr{p}, me, work)

	p.ReviewRequestedFromUsers = []string{"other", me}
	got, seen := Diff(seen, []types.ViewPr{p}, me, work)
	if len(got) != 1 {
		t.Fatalf("review request: got %v, want one", got)
	}

	p.ReviewRequestedFromUsers = nil
	p.RereviewFrom = []string{me}
	got, _ = Diff(seen, []types.ViewPr{p}, me, work)
	if len(got) != 0 {
		t.Errorf("request turning into re-review is the same reason, got %v", got)
	}
}

func TestScoreOnlyChangesOnWorkPrsStayQuiet(t *testing.T) {
	p := pr("a", "work", "someone")
	_, seen := Diff(map[string][]string{}, []types.ViewPr{p}, me, work)

	p.HeadRefOid = "abc"
	p.LastPrCommenter = "someone"
	p.LastUpdated = p.LastUpdated.Add(time.Hour)
	p.Additions = 500
	got, _ := Diff(seen, []types.ViewPr{p}, me, work)
	if len(got) != 0 {
		t.Errorf("a push and a comment on someone else's work PR notified: %v", got)
	}
}

func TestOthersCommentOnOwnWorkPr(t *testing.T) {
	p := pr("a", "work", me)
	p.LastPrCommenter = me
	_, seen := Diff(map[string][]string{}, []types.ViewPr{p}, me, work)

	p.LastPrCommenter = "reviewer"
	got, _ := Diff(seen, []types.ViewPr{p}, me, work)
	if len(got) != 1 || !strings.Contains(got[0].Body, "reviewer") {
		t.Errorf("got %v, want one notification naming the commenter", got)
	}
}

func TestOtherOwnersNotifyOnEveryChangeBySomeoneElse(t *testing.T) {
	p := pr("a", "oss", "someone")
	p.HeadRefOid = "one"
	_, seen := Diff(map[string][]string{}, []types.ViewPr{p}, me, work)

	p.HeadRefOid = "two"
	got, seen := Diff(seen, []types.ViewPr{p}, me, work)
	if len(got) != 1 {
		t.Fatalf("push on an open-source PR: got %v, want one", got)
	}

	p.LastPrCommenter = "someone"
	p.LastUpdated = p.LastUpdated.Add(time.Hour)
	got, seen = Diff(seen, []types.ViewPr{p}, me, work)
	if len(got) != 1 {
		t.Fatalf("comment on an open-source PR: got %v, want one", got)
	}

	p.LastPrCommenter = me
	p.LastUpdated = p.LastUpdated.Add(time.Hour)
	got, _ = Diff(seen, []types.ViewPr{p}, me, work)
	if len(got) != 0 {
		t.Errorf("the user's own comment notified: %v", got)
	}
}

func TestOwnPushOnOpenSourcePrIsQuiet(t *testing.T) {
	p := pr("a", "oss", me)
	p.HeadRefOid = "one"
	_, seen := Diff(map[string][]string{}, []types.ViewPr{p}, me, work)

	p.HeadRefOid = "two"
	got, _ := Diff(seen, []types.ViewPr{p}, me, work)
	if len(got) != 0 {
		t.Errorf("the user's own push notified: %v", got)
	}
}

func TestBuriedPrsNeverNotify(t *testing.T) {
	p := pr("a", "work", me)
	p.Buried = true
	_, seen := Diff(map[string][]string{}, []types.ViewPr{p}, me, work)

	p.ReviewStatus = "APPROVED"
	got, _ := Diff(seen, []types.ViewPr{p}, me, work)
	if len(got) != 0 {
		t.Errorf("buried PR notified: %v", got)
	}
}

func TestGonePrsAreForgotten(t *testing.T) {
	p := pr("a", "work", me)
	_, seen := Diff(map[string][]string{}, []types.ViewPr{p}, me, work)
	_, seen = Diff(seen, nil, me, work)
	if _, ok := seen["a"]; ok {
		t.Error("a PR elly no longer lists stays in the seen state forever")
	}
}

func TestCoalesce(t *testing.T) {
	ns := []Notification{{Title: "1"}, {Title: "2"}, {Title: "3"}}
	if got := Coalesce(ns); len(got) != 3 {
		t.Errorf("three notifications coalesced: %v", got)
	}
	ns = append(ns, Notification{Title: "4"})
	got := Coalesce(ns)
	if len(got) != 1 || !strings.Contains(got[0].Body, "4") {
		t.Errorf("four notifications: got %v, want one summary naming 4", got)
	}
}

func TestEscape(t *testing.T) {
	if got := escape(`<b>safe</b> & "x"`); got != "&lt;b&gt;safe&lt;/b&gt; &amp; &#34;x&#34;" {
		t.Errorf("escape: %q", got)
	}
}

func TestSenderPassesEscapedText(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	bin := filepath.Join(dir, "notify-send")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsFile + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	Sender{Bin: bin, Logger: slog.New(slog.DiscardHandler)}.Send(Notification{Title: "<b>safe to merge</b>", Body: "a & b"})

	var args []byte
	for range 100 {
		if b, err := os.ReadFile(argsFile); err == nil && len(b) > 0 {
			args = b
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	got := string(args)
	if !strings.Contains(got, "&lt;b&gt;safe to merge&lt;/b&gt;\n") || !strings.Contains(got, "a &amp; b\n") || strings.Contains(got, "<b>") {
		t.Errorf("notify-send got %q", got)
	}
}
