package github

import (
	"bytes"
	"encoding/json"
	"flag"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chelmertz/elly/anonymise"
	"github.com/chelmertz/elly/internal/points"
	"github.com/chelmertz/elly/internal/types"
	"github.com/google/go-cmp/cmp"
)

var update = flag.Bool("update", false, "rewrite testdata/recordings/*.want.json from the current code")

// A recording is one real poll, anonymised by elly -record. Replaying it runs
// the same requests, the same parsing and the same scoring a live poll runs,
// so a wrong guess about github's response shape cannot pass here the way it
// can in a test that builds the response by hand.
//
// When a query changes, the recordings stop matching and -update cannot help,
// since the responses lack whatever the new query asks for: re-record.

const recordingsDir = "testdata/recordings"

type goldenPr struct {
	Pr     types.ViewPr
	Points points.Points
}

type goldenResult struct {
	Prs          []goldenPr
	Degradations []types.Degradation
}

func recordingPaths(t *testing.T) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(recordingsDir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, p := range paths {
		if !strings.HasSuffix(p, ".want.json") {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		t.Fatalf("no recordings in %s; make one with elly -record %s -record-owners <owner>", recordingsDir, recordingsDir)
	}
	return out
}

func loadRecording(t *testing.T, path string) recording {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rec recording
	if err := json.Unmarshal(body, &rec); err != nil {
		t.Fatal(err)
	}
	return rec
}

func canonical(t *testing.T, b []byte) any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("not json: %v", err)
	}
	return v
}

// replay serves rec in order, failing on any request that differs from the
// recorded one, and returns what the poll and the scoring made of it.
func replay(t *testing.T, rec recording) goldenResult {
	t.Helper()
	var mu sync.Mutex
	next := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		body, _ := io.ReadAll(r.Body)
		if next >= len(rec.Exchanges) {
			t.Errorf("request %d was not recorded: %s", next+1, body)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		ex := rec.Exchanges[next]
		next++
		if diff := cmp.Diff(canonical(t, ex.Request), canonical(t, body)); diff != "" {
			t.Errorf("request %d differs from the recorded one (-recorded +sent):\n%s", next, diff)
		}
		w.WriteHeader(ex.Status)
		_, _ = w.Write(ex.Response)
	}))
	defer srv.Close()

	prs, degradations, err := queryGithub(srv.URL, "token", "me", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("replayed poll failed: %v", err)
	}
	if next != len(rec.Exchanges) {
		t.Errorf("the poll made %d of the %d recorded requests", next, len(rec.Exchanges))
	}
	out := goldenResult{Prs: []goldenPr{}, Degradations: degradations}
	for _, pr := range prs {
		pr.RawJsonResponse = nil // the search node again, in the recording already
		out.Prs = append(out.Prs, goldenPr{Pr: pr, Points: *points.StandardPrPoints(pr, "me", rec.PollTime)})
	}
	for i := range out.Degradations {
		out.Degradations[i].Seen = time.Time{} // when the test ran
	}
	return out
}

func Test_Recordings_ReplayToTheExpectedPrsAndPoints(t *testing.T) {
	for _, path := range recordingPaths(t) {
		t.Run(filepath.Base(path), func(t *testing.T) {
			b, err := json.MarshalIndent(replay(t, loadRecording(t, path)), "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			// compared as the file holds it, so both sides went through JSON
			var got goldenResult
			if err := json.Unmarshal(b, &got); err != nil {
				t.Fatal(err)
			}
			wantPath := strings.TrimSuffix(path, ".json") + ".want.json"
			if *update {
				if err := os.WriteFile(wantPath, append(b, '\n'), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			body, err := os.ReadFile(wantPath)
			if err != nil {
				t.Fatalf("%v; run go test ./internal/github -update to create it", err)
			}
			var want goldenResult
			if err := json.Unmarshal(body, &want); err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("replay differs from %s (-want +got), rerun with -update if intended:\n%s", wantPath, diff)
			}
		})
	}
}

// The committed recordings are checked without the raw input they came from:
// every string must have the form elly's policy publishes at its path, so a
// real name, title or id that got past the anonymiser fails here.
func Test_Recordings_HoldOnlyPublishedForms(t *testing.T) {
	for _, path := range recordingPaths(t) {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := anonymise.CheckForms(publishedRules(), body); err != nil {
			t.Errorf("%s: %v", path, err)
		}
	}
}

// Named PRs in poll-1 that stand in for hand-written tests whose only job was
// to build a response of the right shape. If a re-recording drops them, pick
// PRs that cover the same path rather than deleting the assertion.
func Test_Poll1_PinnedPrs(t *testing.T) {
	path := filepath.Join(recordingsDir, "poll-1.json")
	rec := loadRecording(t, path)
	got := replay(t, rec)
	find := func(url string) types.ViewPr {
		t.Helper()
		for _, p := range got.Prs {
			if p.Pr.Url == url {
				return p.Pr
			}
		}
		t.Fatalf("%s is not in %s", url, path)
		return types.ViewPr{}
	}
	ownWithThread := "https://github.com/owner1/repo1/pull/1"

	// The search query aliases firstComment and lastComment; their keys must
	// land in the fields actionableThreads reads. This PR's single thread
	// sits on the search's first page, and someone else has the last word.
	recorded := string(rec.Exchanges[0].Response)
	if !strings.Contains(recorded, `"firstComment"`) || !strings.Contains(recorded, `"lastComment"`) {
		t.Fatal("the recording no longer carries the aliased thread comments")
	}
	if pr := find(ownWithThread); pr.Author != "me" || pr.ThreadsActionable != 1 {
		t.Errorf("own PR with one answered thread: author %q, %d actionable, want me, 1", pr.Author, pr.ThreadsActionable)
	}

	// The head commit is how a consumer tells new commits from new chatter.
	if pr := find(ownWithThread); pr.HeadRefOid != "0000000000000000000000000000000000000001" {
		t.Errorf("head commit = %q, want the recorded headRefOid", pr.HeadRefOid)
	}
}

// More of poll-1 standing in for hand-written wiring tests. Each pin names the
// PR or the recorded request that exercises the path.
func Test_Poll1_PinnedWiring(t *testing.T) {
	path := filepath.Join(recordingsDir, "poll-1.json")
	rec := loadRecording(t, path)
	got := replay(t, rec)
	prs := map[string]types.ViewPr{}
	for _, p := range got.Prs {
		prs[p.Pr.Url] = p.Pr
	}
	var search [2]querySearchPrsInvolvingMeGraphQl
	for i := range search {
		if err := json.Unmarshal(rec.Exchanges[i].Response, &search[i]); err != nil {
			t.Fatal(err)
		}
	}
	node := func(url string) prSearchResultGraphQl {
		t.Helper()
		for _, e := range search[0].Data.Search.Edges {
			var pr prSearchResultGraphQl
			if err := json.Unmarshal(e.Node, &pr); err != nil {
				t.Fatal(err)
			}
			if pr.Url == url {
				return pr
			}
		}
		t.Fatalf("%s is not in the involves: search of %s", url, path)
		return prSearchResultGraphQl{}
	}

	t.Run("query text", func(t *testing.T) {
		// What the search asks for - headRefOid, the Team reviewer's name,
		// only the first and last comment of a thread, no bodies - is pinned
		// by the replay's exact request match: any change to the query text
		// makes every recorded request differ. The recording must therefore
		// hold the query as the code sends it.
		for i, ex := range rec.Exchanges[:2] {
			var q graphqlQuery
			if err := json.Unmarshal(ex.Request, &q); err != nil || q.Query != searchPrsQuery {
				t.Errorf("exchange %d is not the search query as the code sends it", i+1)
			}
		}
	})

	t.Run("a PR in both searches is one row", func(t *testing.T) {
		both := "https://github.com/owner1/repo5/pull/9"
		if len(search[1].Data.Search.Edges) != 1 {
			t.Fatalf("the review-requested: search should hold exactly %s", both)
		}
		node(both) // it is in the involves: search too
		n := 0
		for _, p := range got.Prs {
			if p.Pr.Url == both {
				n++
			}
		}
		if n != 1 || len(got.Prs) != len(search[0].Data.Search.Edges) {
			t.Errorf("%s appears %d times, %d rows from %d involves: results", both, n, len(got.Prs), len(search[0].Data.Search.Edges))
		}
	})

	t.Run("mergeability is asked for own PRs only, in one batch", func(t *testing.T) {
		// The replay matches the recorded batch exactly, so asking for
		// anyone else's PR, or one request per PR, fails there; this
		// states what the recorded batch is.
		var batches [][]string
		for _, ex := range rec.Exchanges {
			var q struct {
				Query     string
				Variables struct{ Ids []string }
			}
			if err := json.Unmarshal(ex.Request, &q); err != nil {
				t.Fatal(err)
			}
			if q.Query == mergeabilityQuery {
				batches = append(batches, q.Variables.Ids)
			}
		}
		var own []string
		for _, e := range search[0].Data.Search.Edges {
			var pr prSearchResultGraphQl
			_ = json.Unmarshal(e.Node, &pr)
			if pr.Author.Login == "me" {
				own = append(own, pr.Id)
			}
		}
		slices.Sort(own)
		if len(batches) != 1 || !slices.Equal(batches[0], own) {
			t.Errorf("mergeability batches %v, want one batch of the %d own PRs", batches, len(own))
		}
		if pr := prs["https://github.com/owner1/repo5/pull/23"]; pr.Mergeable != "" {
			t.Errorf("someone else's PR was never asked about, so its mergeability must stay unknown, got %q", pr.Mergeable)
		}
		if pr := prs["https://github.com/owner1/repo1/pull/1"]; pr.Mergeable == "" {
			t.Error("an own PR must carry the answer")
		}
	})

	t.Run("CI bots never take the last-commenter slot", func(t *testing.T) {
		// Only bots commented on these; elly keeps the slot empty.
		for _, url := range []string{"https://github.com/owner1/repo1/pull/15", "https://github.com/owner1/repo5/pull/13"} {
			if pr := prs[url]; pr.LastPrCommenter != "" {
				t.Errorf("%s: last commenter %q, want none", url, pr.LastPrCommenter)
			}
		}
	})
}
