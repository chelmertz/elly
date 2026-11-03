package notify

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	rank := func(string) int { return 0 }
	if got := Coalesce(ns, rank); len(got) != 3 {
		t.Errorf("three notifications coalesced: %v", got)
	}
	ns = append(ns, Notification{Title: "4"})
	got := Coalesce(ns, rank)
	if len(got) != 1 || !strings.Contains(got[0].Body, "4") {
		t.Errorf("four notifications: got %v, want one summary naming 4", got)
	}
}

func TestCoalescedNotificationKeepsTheHighestRankedPr(t *testing.T) {
	ns := []Notification{
		{PrUrl: "https://github.com/o/r/pull/1"},
		{PrUrl: "https://github.com/o/r/pull/2"},
		{PrUrl: "https://github.com/o/r/pull/3"},
		{PrUrl: "https://github.com/o/r/pull/4"},
	}
	pos := map[string]int{ns[0].PrUrl: 3, ns[1].PrUrl: 1, ns[2].PrUrl: 0, ns[3].PrUrl: 2}
	got := Coalesce(ns, func(u string) int { return pos[u] })
	if len(got) != 1 || got[0].PrUrl != "https://github.com/o/r/pull/3" {
		t.Errorf("got %v, want one summary pointing at pull/3", got)
	}
}

func TestClickingANotificationOpensItsRow(t *testing.T) {
	s := Sender{PageUrl: "http://localhost:9876"}
	if got := s.openUrl(Notification{PrUrl: "https://github.com/Owner/repo/pull/11534"}); got != "http://localhost:9876/#pr-owner-repo-11534" {
		t.Errorf("got %q", got)
	}
	if got := s.openUrl(Notification{}); got != "http://localhost:9876/" {
		t.Errorf("without a PR: got %q", got)
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

func TestAnnotationsNeverNotify(t *testing.T) {
	p := pr("a", "other", "someone")
	_, seen := Diff(map[string][]string{}, []types.ViewPr{p}, me, work)
	for _, state := range []string{types.AnnotationRunning, types.AnnotationNeedsYou, types.AnnotationStuck, types.AnnotationHandled} {
		annotated := p
		annotated.Annotations = []types.Annotation{{Source: "claude-review", State: state, Note: "started by tick"}}
		if got, _ := Diff(seen, []types.ViewPr{annotated}, me, work); len(got) != 0 {
			t.Errorf("a %s annotation notified: %v", state, got)
		}
	}
}

// A PR that is new to elly but asks nothing of the user, on a work owner,
// stays quiet; a question on it notifies.
func TestNewWorkPrNotifiesOnlyForAReason(t *testing.T) {
	_, seen := Diff(map[string][]string{}, nil, me, work)
	quiet := pr("a", "work", "someone")
	got, seen := Diff(seen, []types.ViewPr{quiet}, me, work)
	if len(got) != 0 {
		t.Fatalf("a PR with no reason notified: %v", got)
	}
	quiet.ThreadsActionable = 1
	got, _ = Diff(seen, []types.ViewPr{quiet}, me, work)
	if len(got) != 1 || !strings.Contains(got[0].Body, "asked us something") {
		t.Errorf("a question: got %v, want one notification", got)
	}
}

// On an open-source PR a review decision by someone else notifies; no
// decision is no reason.
func TestOtherOwnersNotifyOnAReviewDecision(t *testing.T) {
	p := pr("a", "oss", "someone")
	_, seen := Diff(map[string][]string{}, []types.ViewPr{p}, me, work)
	if len(seen["a"]) != 0 {
		t.Fatalf("an open-source PR with nothing on it has reasons %v", seen["a"])
	}
	p.ReviewStatus = "CHANGES_REQUESTED"
	got, _ := Diff(seen, []types.ViewPr{p}, me, work)
	if len(got) != 1 || got[0].Body != "Review status: changes requested" {
		t.Errorf("got %v, want one review-status notification", got)
	}
}

// PRs rank cannot place all share one rank; the summary then keeps the first.
func TestCoalescedNotificationKeepsTheFirstOnATie(t *testing.T) {
	ns := []Notification{{PrUrl: "1"}, {PrUrl: "2"}, {PrUrl: "3"}, {PrUrl: "4"}}
	if got := Coalesce(ns, func(string) int { return 0 }); got[0].PrUrl != "1" {
		t.Errorf("got %v, want the summary to point at the first", got)
	}
}

// A click (notify-send printing the action name) opens the PR's row; a
// dismissal opens nothing.
func TestSenderOpensThePageOnlyOnAClick(t *testing.T) {
	for _, c := range []struct {
		printed string
		opened  bool
	}{{"default", true}, {"", false}} {
		dir := t.TempDir()
		opened := filepath.Join(dir, "opened")
		write := func(name, script string) string {
			p := filepath.Join(dir, name)
			if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script), 0o700); err != nil {
				t.Fatal(err)
			}
			return p
		}
		bin := write("notify-send", "echo '"+c.printed+"'\n")
		write("xdg-open", "printf '%s' \"$1\" > "+opened+"\n")
		t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
		var logs lockedBuffer
		Sender{Bin: bin, PageUrl: "http://localhost:9876", Logger: slog.New(slog.NewTextHandler(&logs, nil))}.Send(Notification{PrUrl: "https://github.com/o/r/pull/1"})
		var got []byte
		for range 100 {
			if b, err := os.ReadFile(opened); err == nil && len(b) > 0 {
				got = b
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if c.opened && string(got) != "http://localhost:9876/#pr-o-r-1" {
			t.Errorf("a click opened %q", got)
		}
		if !c.opened && got != nil {
			t.Errorf("a dismissal opened %q", got)
		}
		time.Sleep(50 * time.Millisecond) // Send logs right after starting xdg-open
		if l := logs.String(); strings.Contains(l, "level=WARN") {
			t.Errorf("a working notify-send and xdg-open warned:\n%s", l)
		}
	}
}

type lockedBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}
