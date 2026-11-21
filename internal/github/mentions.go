package github

import (
	"crypto/sha256"
	"encoding/json"
	"encoding/hex"
	"regexp"
	"strings"
	"time"

	"github.com/chelmertz/elly/internal/types"
)

// mentionable are the author associations whose @-mention waits on the
// user: people in the org or on the repo. Anyone else can comment on a
// public repo, and a mention from them is not a colleague's question.
var mentionable = map[string]bool{"OWNER": true, "MEMBER": true, "COLLABORATOR": true}

var inlineCode = regexp.MustCompile("`[^`\n]*`")

// mentionedIn says whether body names @user outside quoted lines, code
// fences and code spans. A login is word-bounded on both sides: "@meow"
// and "me@me.com" do not name "me".
func mentionedIn(body, user string) bool {
	if user == "" {
		return false
	}
	at := regexp.MustCompile(`(?i)(^|[^\w@.])@` + regexp.QuoteMeta(user) + `($|[^\w-])`)
	fenced := false
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			fenced = !fenced
			continue
		}
		if fenced || strings.HasPrefix(trimmed, ">") {
			continue
		}
		if at.MatchString(inlineCode.ReplaceAllString(line, "")) {
			return true
		}
	}
	return false
}

// waitsOnUser is a comment that mentions user, written by a colleague (not
// the user, not a bot) whom the user has not answered with a reaction.
func waitsOnUser(author, assoc, body string, reactions prReviewThreadCommentReactionGraphQl, user string) bool {
	return author != "" && !strings.EqualFold(author, user) && !strings.HasSuffix(author, "[bot]") &&
		mentionable[assoc] && !userReactedToComment(reactions, user) && mentionedIn(body, user)
}

func mentionOf(url, author, createdAt, body string) types.Mention {
	at, _ := time.Parse(time.RFC3339, createdAt)
	sum := sha256.Sum256([]byte(body))
	return types.Mention{Url: url, Author: author, At: at, BodyHash: hex.EncodeToString(sum[:8])}
}

// mentions are the PR's open @-mentions of user. A thread's mention is open
// while it is the last comment of an unresolved thread: any reply by the
// user's login, a signed Claude answer included, ends it. A PR-level one is
// open while no later comment among the last five is by the user's login.
// A reaction by the user ends either.
func mentions(pr prSearchResultGraphQl, user string) []types.Mention {
	var out []types.Mention
	for _, t := range pr.ReviewThreads.Edges {
		if t.Node.IsResolved || len(t.Node.LastComment.Nodes) == 0 {
			continue
		}
		c := t.Node.LastComment.Nodes[0]
		if waitsOnUser(c.Author.Login, c.AuthorAssociation, c.Body, c.Reactions, user) {
			out = append(out, mentionOf(c.Url, c.Author.Login, c.CreatedAt, c.Body))
		}
	}
	answered := false
	edges := pr.Comments.Edges
	for i := len(edges) - 1; i >= 0; i-- {
		c := edges[i].Node
		if strings.EqualFold(c.Author.Login, user) {
			answered = true
			continue
		}
		if !answered && waitsOnUser(c.Author.Login, c.AuthorAssociation, c.Body, c.Reactions, user) {
			out = append(out, mentionOf(c.Url, c.Author.Login, c.CreatedAt, c.Body))
		}
	}
	return out
}

// withoutBodies is the PR's raw search node with every comment "body"
// removed. The raw node is stored and served by GET /api/v0/prs, which any
// local process can read; the bodies are fetched only to find mentions.
// A node that does not parse is dropped whole rather than kept with text.
func withoutBodies(raw json.RawMessage) json.RawMessage {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return nil
	}
	var strip func(any)
	strip = func(x any) {
		switch t := x.(type) {
		case map[string]any:
			delete(t, "body")
			for _, c := range t {
				strip(c)
			}
		case []any:
			for _, c := range t {
				strip(c)
			}
		}
	}
	strip(v)
	out, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return out
}
