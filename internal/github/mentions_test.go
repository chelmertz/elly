package github

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func mentionComment(author, assoc, body string, reactors ...string) prReviewThreadCommentGraphQl {
	c := prReviewThreadCommentGraphQl{Url: "https://github.com/o/r/pull/1#discussion_r1", Body: body, AuthorAssociation: assoc, CreatedAt: "2026-11-21T10:00:00Z"}
	c.Author.Login = author
	for _, r := range reactors {
		e := struct {
			Node struct {
				Content string
				User    struct{ Login string }
			}
		}{}
		e.Node.User.Login = r
		c.Reactions.Edges = append(c.Reactions.Edges, e)
	}
	return c
}

func TestMentionedIn(t *testing.T) {
	cases := map[string]bool{
		"@me can you check":           true,
		"hey @Me, thoughts?":          true,
		"cc @me.":                     true,
		"@meow is someone else":       false,
		"mail me@me.com":              false,
		"> @me quoted\nno ask":        false,
		"see `@me` in code":           false,
		"```\n@me\n```\nfenced":       false,
		"quote\n> old @me\n\n@me new": true,
	}
	for body, want := range cases {
		if got := mentionedIn(body, "me"); got != want {
			t.Errorf("mentionedIn(%q) = %v", body, got)
		}
	}
}

func TestMentionsInThreads(t *testing.T) {
	open := mentionComment("adam", "MEMBER", "@me why?")
	cases := []struct {
		name   string
		thread prReviewThreadGraphQl
		want   int
	}{
		{"member asks, last comment", threadOf(commentBy("x"), open), 1},
		{"user replied after", threadOf(open, commentBy("me")), 0},
		{"user reacted", threadOf(mentionComment("adam", "MEMBER", "@me why?", "me")), 0},
		{"someone else reacted", threadOf(mentionComment("adam", "MEMBER", "@me why?", "eve")), 1},
		{"outside contributor", threadOf(mentionComment("adam", "CONTRIBUTOR", "@me why?")), 0},
		{"bot", threadOf(mentionComment("ci[bot]", "MEMBER", "@me why?")), 0},
		{"user mentions self", threadOf(mentionComment("me", "MEMBER", "@me note")), 0},
		{"no mention", threadOf(mentionComment("adam", "MEMBER", "looks fine")), 0},
	}
	for _, c := range cases {
		got := mentions(prWithThread("author", c.thread), "me")
		if len(got) != c.want {
			t.Errorf("%s: %d mentions, want %d", c.name, len(got), c.want)
		}
	}
	resolved := threadOf(open)
	resolved.IsResolved = true
	if got := mentions(prWithThread("author", resolved), "me"); len(got) != 0 {
		t.Errorf("a resolved thread's mention is open: %v", got)
	}
	m := mentions(prWithThread("author", threadOf(open)), "me")[0]
	if m.Url != open.Url || m.Author != "adam" || m.BodyHash == "" || m.At.IsZero() {
		t.Errorf("mention %+v", m)
	}
	edited := mentions(prWithThread("author", threadOf(mentionComment("adam", "MEMBER", "@me why now?"))), "me")[0]
	if edited.BodyHash == m.BodyHash {
		t.Error("an edit keeps the hash")
	}
}

func prWithComments(cs ...prReviewThreadCommentGraphQl) prSearchResultGraphQl {
	var pr prSearchResultGraphQl
	pr.Author.Login = "author"
	for _, c := range cs {
		e := struct{ Node prCommentGraphQl }{}
		e.Node.Author.Login = c.Author.Login
		e.Node.Url, e.Node.Body, e.Node.AuthorAssociation, e.Node.CreatedAt, e.Node.Reactions = c.Url, c.Body, c.AuthorAssociation, c.CreatedAt, c.Reactions
		pr.Comments.Edges = append(pr.Comments.Edges, e)
	}
	return pr
}

func TestMentionsInPrComments(t *testing.T) {
	ask := mentionComment("adam", "OWNER", "@me ping")
	plain := mentionComment("eve", "MEMBER", "unrelated")
	if got := mentions(prWithComments(ask, plain), "me"); len(got) != 1 {
		t.Errorf("a later comment by someone else answers nothing: %v", got)
	}
	if got := mentions(prWithComments(ask, commentFrom("me")), "me"); len(got) != 0 {
		t.Errorf("the user's later comment answers it: %v", got)
	}
	if got := mentions(prWithComments(commentFrom("me"), ask), "me"); len(got) != 1 {
		t.Errorf("an earlier comment by the user answers nothing: %v", got)
	}
	if got := mentions(prWithComments(mentionComment("adam", "OWNER", "@me ping", "me")), "me"); len(got) != 0 {
		t.Errorf("a reaction answers it: %v", got)
	}
}

func commentFrom(login string) prReviewThreadCommentGraphQl {
	return mentionComment(login, "MEMBER", "answer\n\n--claude")
}

func TestWithoutBodiesDropsEveryBody(t *testing.T) {
	raw := []byte(`{"title":"t","comments":{"edges":[{"node":{"body":"@me secret","url":"u"}}]},"reviewThreads":{"edges":[{"node":{"lastComment":{"nodes":[{"body":"x"}]}}}]}}`)
	got := string(withoutBodies(raw))
	if strings.Contains(got, "body") || strings.Contains(got, "secret") || !strings.Contains(got, `"url":"u"`) {
		t.Errorf("got %s", got)
	}
	if withoutBodies([]byte("{broken")) != nil {
		t.Error("an unparsable node is kept")
	}
}

// End to end through a replayed poll: comments given a member's @-mention
// come out as Mentions, and the stored raw node keeps none of their text.
func TestPollFindsMentionsAndStoresNoBodies(t *testing.T) {
	rec := loadRecording(t, filepath.Join(recordingsDir, "poll-1.json"))
	for i, ex := range rec.Exchanges {
		var v any
		if json.Unmarshal(ex.Response, &v) != nil {
			continue
		}
		var walk func(any)
		walk = func(x any) {
			switch n := x.(type) {
			case map[string]any:
				if _, ok := n["author"]; ok {
					if _, ok := n["url"]; ok {
						n["body"] = "@me secret question"
						n["authorAssociation"] = "MEMBER"
					}
				}
				for _, c := range n {
					walk(c)
				}
			case []any:
				for _, c := range n {
					walk(c)
				}
			}
		}
		walk(v)
		rec.Exchanges[i].Response, _ = json.Marshal(v)
	}
	replay(t, rec)
	found := 0
	for _, pr := range lastReplayPrs {
		found += len(pr.Mentions)
		if strings.Contains(string(pr.RawJsonResponse), "secret") {
			t.Errorf("%s keeps a comment body in its raw node", pr.Url)
		}
	}
	if found == 0 {
		t.Error("no mention found in a poll whose comments all mention the user")
	}
}
