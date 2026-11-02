package anonymise

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

// The policy below is a synthetic caller's, for a made-up forge at
// forge.example. It exercises every mechanism the package offers: counters
// with fixed values, suffixes, sorting and a pattern, and rewrites that map
// the parts of a URL and of a search string.

const testQuery = "query($id: ID!) { node(id: $id) { title } }"

var (
	login  = Rule{Class: Counter, Kind: "login", Prefix: "user", Fixed: map[string]string{"realme": "me", "ci-bot": "ci-bot"}, Suffixes: []string{"[bot]"}}
	owner  = Rule{Class: Counter, Kind: "owner", Prefix: "owner"}
	repo   = Rule{Class: Counter, Kind: "repo", Prefix: "repo"}
	nodeID = Rule{Class: Counter, Kind: "node id", Prefix: "ID_", Format: "%06d", Sorted: true}
	sha    = Rule{Class: Counter, Kind: "sha", Format: "%040d", Pattern: regexp.MustCompile(`^[0-9a-f]{40}$`)}
	cursor = Rule{Class: Counter, Kind: "cursor", Prefix: "cursor"}

	testURLRe = regexp.MustCompile(`^https://forge\.example/([^/?#]+)/([^/?#]+)(?:/issues/([0-9]+)(?:#note-([0-9]+))?)?$`)
	testURL   = Rule{Class: Func, Kind: "url", Rewrite: func(m *Mapper, raw string) (string, error) {
		p := testURLRe.FindStringSubmatch(raw)
		if p == nil {
			return "", errors.New("url value is not a known form")
		}
		o, _ := m.Apply(owner, p[1])
		r, _ := m.Apply(repo, p[2])
		out := "https://forge.example/" + o + "/" + r
		if p[3] != "" {
			out += fmt.Sprintf("/issues/%d", m.Number("issue", p[1]+"/"+p[2]+"#"+p[3]))
		}
		if p[4] != "" {
			out += fmt.Sprintf("#note-%d", m.Number("note", p[4]))
		}
		return out, nil
	}, Form: regexp.MustCompile(`^https://forge\.example/` + CounterPattern(owner) + `/` + CounterPattern(repo) + `(?:/issues/[0-9]+(?:#note-[0-9]+)?)?$`)}

	testSearch = Rule{Class: Func, Kind: "search", Rewrite: func(m *Mapper, raw string) (string, error) {
		qualifiers := map[string]Rule{"state": EnumOf("open"), "involves": login}
		tokens := strings.Split(raw, " ")
		for i, tok := range tokens {
			k, v, _ := strings.Cut(tok, ":")
			rule, ok := qualifiers[k]
			if !ok {
				return "", errors.New("search value has an unlisted qualifier")
			}
			mapped, err := m.Apply(rule, v)
			if err != nil {
				return "", err
			}
			tokens[i] = k + ":" + mapped
		}
		return strings.Join(tokens, " "), nil
	}, Form: regexp.MustCompile(`^state:open involves:` + CounterPattern(login) + `$`)}
)

func testRules() Rules {
	return Rules{
		Paths: map[string]Rule{
			"pollTime":                   As(Timestamp),
			"requests[].query":           ConstantOf(testQuery),
			"requests[].variables.q":     testSearch,
			"requests[].variables.id":    nodeID,
			"requests[].variables.ids[]": nodeID,
			"requests[].variables.after": cursor,
			"items[].id":                 nodeID,
			"items[].title":              As(Redact),
			"items[].url":                testURL,
			"items[].repo.url":           testURL,
			"items[].repo.name":          repo,
			"items[].repo.owner":         owner,
			"items[].author":             login,
			"items[].state":              EnumOf("OPEN", "MERGED"),
			"items[].sha":                sha,
			"items[].at":                 As(Timestamp),
			"items[].additions":          As(Keep),
			"items[].draft":              As(Keep),
			"items[].cursor":             cursor,
			"items[].notes[].url":        testURL,
		},
		Vocabulary: []string{"https://forge.example/", "/issues/", "#note-", "state:open", "involves:"},
	}
}

func run(t *testing.T, doc string) map[string]any {
	t.Helper()
	out, err := Anonymise(testRules(), []byte(doc))
	if err != nil {
		t.Fatalf("Anonymise: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func items(doc map[string]any) []map[string]any {
	var out []map[string]any
	for _, p := range doc["items"].([]any) {
		out = append(out, p.(map[string]any))
	}
	return out
}

func request(doc map[string]any) map[string]any {
	return doc["requests"].([]any)[0].(map[string]any)
}

func Test_ValuesMapConsistentlyAcrossRequestsAndResponses(t *testing.T) {
	got := run(t, `{
		"requests": [{"query": "`+testQuery+`", "variables": {"q": "state:open involves:alice"}}],
		"items": [
			{"author": "alice"}, {"author": "realme"}, {"author": "alice"},
			{"author": "ci-bot"}, {"author": "bob"}, {"author": "helper[bot]"}, {"author": ""}
		]}`)
	var authors []string
	for _, p := range items(got) {
		authors = append(authors, p["author"].(string))
	}
	want := []string{"user1", "me", "user1", "ci-bot", "user2", "user3[bot]", ""}
	if diff := cmp.Diff(want, authors); diff != "" {
		t.Errorf("authors (-want +got):\n%s", diff)
	}
	if q := request(got)["variables"].(map[string]any)["q"]; q != "state:open involves:user1" {
		t.Errorf("the login in the request must map like the one in the response, got %q", q)
	}
	if request(got)["query"] != testQuery {
		t.Error("a constant must be kept as is")
	}
}

// A caller sorting mapped values must see them in the order the raw ones had,
// or a replayed request would differ from the recorded one.
func Test_SortedCountersKeepTheSortOrder(t *testing.T) {
	got := run(t, `{
		"requests": [{"variables": {"ids": ["N_zzz", "N_aaa", "N_mmm"]}}],
		"items": [{"id": "N_mmm"}, {"id": "N_aaa"}, {"id": "N_zzz"}]}`)
	ids := request(got)["variables"].(map[string]any)["ids"].([]any)
	if diff := cmp.Diff([]any{"ID_000003", "ID_000001", "ID_000002"}, ids); diff != "" {
		t.Errorf("request ids (-want +got):\n%s", diff)
	}
	var resp []string
	for _, p := range items(got) {
		resp = append(resp, p["id"].(string))
	}
	if diff := cmp.Diff([]string{"ID_000002", "ID_000001", "ID_000003"}, resp); diff != "" {
		t.Errorf("response ids (-want +got):\n%s", diff)
	}
}

func Test_TimestampsShiftByOneOffset(t *testing.T) {
	in := []string{"2026-09-08T10:00:00Z", "2026-09-01T23:59:30+02:00", "2026-10-31T00:00:00Z"}
	got := run(t, `{"pollTime": "`+in[0]+`", "items": [{"at": "`+in[1]+`"}, {"at": "`+in[2]+`"}, {"at": ""}]}`)
	parse := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	offset := parse(got["pollTime"].(string)).Sub(parse(in[0]))
	if offset == 0 {
		t.Fatal("timestamps were not shifted")
	}
	for i, p := range items(got)[:2] {
		if d := parse(p["at"].(string)).Sub(parse(in[i+1])); d != offset {
			t.Errorf("timestamp %d shifted by %v, poll time by %v", i, d, offset)
		}
	}
	if items(got)[2]["at"] != "" {
		t.Error("an empty timestamp stays empty")
	}
	if offset > -30*24*time.Hour || offset < -731*24*time.Hour {
		t.Errorf("offset %v is outside 30 days to two years back", offset)
	}
	if again := run(t, `{"pollTime": "`+in[0]+`"}`); again["pollTime"] == got["pollTime"] {
		t.Error("two documents got the same offset")
	}
}

// A rewrite maps the parts of a composite value through the same table, so
// the owner in a URL is the owner in the owner field.
func Test_RewritesMapPartsThroughTheSameTable(t *testing.T) {
	got := run(t, `{"items": [
		{"repo": {"owner": "acme", "name": "rocket", "url": "https://forge.example/acme/rocket"},
		 "url": "https://forge.example/acme/rocket/issues/4711",
		 "notes": [
			{"url": "https://forge.example/acme/rocket/issues/4711#note-99887766"},
			{"url": "https://forge.example/acme/rocket/issues/4711#note-99887766"},
			{"url": "https://forge.example/acme/rocket/issues/4711#note-5544"}
		 ]},
		{"url": "https://forge.example/acme/other/issues/4711"},
		{"url": "https://forge.example/acme/rocket/issues/12"}
	]}`)
	p := items(got)
	want := []string{
		"https://forge.example/owner1/repo1",
		"https://forge.example/owner1/repo1/issues/1",
		"https://forge.example/owner1/repo1/issues/1#note-1",
		"https://forge.example/owner1/repo1/issues/1#note-1",
		"https://forge.example/owner1/repo1/issues/1#note-2",
		"https://forge.example/owner1/repo2/issues/2",
		"https://forge.example/owner1/repo1/issues/3",
	}
	urls := []string{p[0]["repo"].(map[string]any)["url"].(string), p[0]["url"].(string)}
	for _, c := range p[0]["notes"].([]any) {
		urls = append(urls, c.(map[string]any)["url"].(string))
	}
	urls = append(urls, p[1]["url"].(string), p[2]["url"].(string))
	if diff := cmp.Diff(want, urls); diff != "" {
		t.Errorf("urls (-want +got):\n%s", diff)
	}
	if r := p[0]["repo"].(map[string]any); r["owner"] != "owner1" || r["name"] != "repo1" {
		t.Errorf("owner and repo must map like the url parts: %v", r)
	}
}

func Test_EverythingElseIsRedactedOrKept(t *testing.T) {
	got := run(t, `{"items": [
		{"title": "Secret plan", "state": "OPEN", "additions": 12, "draft": true, "sha": "0123456789abcdef0123456789abcdef01234567", "cursor": "Y3Vyc29yOjE="},
		{"title": "Secret plan", "state": "MERGED", "additions": 3, "draft": false, "sha": "fedcba9876543210fedcba9876543210fedcba98", "cursor": "Y3Vyc29yOjI="},
		{"title": "Other", "state": null}
	]}`)
	p := items(got)
	if p[0]["title"] != "redacted-1" || p[1]["title"] != "redacted-1" || p[2]["title"] != "redacted-2" {
		t.Errorf("titles: %v %v %v", p[0]["title"], p[1]["title"], p[2]["title"])
	}
	if p[0]["state"] != "OPEN" || p[2]["state"] != nil || p[0]["additions"] != 12.0 || p[0]["draft"] != true {
		t.Errorf("enums, numbers, booleans and null are kept: %v", p[0])
	}
	if p[0]["sha"] != "0000000000000000000000000000000000000001" || p[1]["sha"] != "0000000000000000000000000000000000000002" {
		t.Errorf("formatted counters: %v %v", p[0]["sha"], p[1]["sha"])
	}
	if p[0]["cursor"] != "cursor1" || p[1]["cursor"] != "cursor2" {
		t.Errorf("cursors: %v %v", p[0]["cursor"], p[1]["cursor"])
	}
}

// Every refusal names the path and the kind and never the value, because the
// value is exactly what must not reach a log.
func Test_RefusalsNameThePathNeverTheValue(t *testing.T) {
	for name, doc := range map[string]string{
		"unknown string path":       `{"items": [{"body": "topsecret"}]}`,
		"unknown number path":       `{"items": [{"databaseId": 31337}]}`,
		"null at unknown path":      `{"items": [{"body": null}]}`,
		"a path a rule only starts": `{"items": [{"auth": null}]}`,
		"value outside the enum":    `{"items": [{"state": "TOPSECRET"}]}`,
		"unregistered constant":     `{"requests": [{"query": "query { topsecret }"}]}`,
		"rewrite refuses":           `{"items": [{"url": "https://elsewhere.example/topsecret"}]}`,
		"unlisted search qualifier": `{"requests": [{"variables": {"q": "org:topsecret involves:realme"}}]}`,
		"counter pattern":           `{"items": [{"sha": "topsecret"}]}`,
		"not a timestamp":           `{"items": [{"at": "topsecret"}]}`,
		"keep on a string":          `{"items": [{"additions": "topsecret"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Anonymise(testRules(), []byte(doc))
			if err == nil {
				t.Fatal("expected a refusal")
			}
			if strings.Contains(strings.ToLower(err.Error()), "topsecret") || strings.Contains(err.Error(), "31337") {
				t.Errorf("the refusal leaks the value: %v", err)
			}
			if !strings.Contains(err.Error(), "items[].") && !strings.Contains(err.Error(), "requests[].") {
				t.Errorf("the refusal must name the path: %v", err)
			}
			if name == "counter pattern" && !strings.Contains(err.Error(), "sha value") {
				t.Errorf("the refusal must name the rule's kind: %v", err)
			}
		})
	}
}

// The leak check is the last line of defence against a mapping bug in the
// caller's rewrites: any value that was replaced must not appear in the
// output, in any case, URL-encoded or not, whatever its length.
func Test_LeakCheckFindsReplacedValues(t *testing.T) {
	secrets := []secret{{"Alice", "login"}, {"Secret plan", "redacted"}, {"plan", "redacted"}, {"x", "login"}, {"issues", "redacted"}}
	published := []string{"user1", "owner1", "repo1", "ID_000001"}
	for _, tc := range []struct {
		name, out string
		leak      bool
	}{
		{"clean output", "https://forge.example/owner1/repo1/issues/1", false},
		{"verbatim", "alice", true},
		{"embedded in a url", "https://forge.example/alice/repo1", true},
		{"embedded in a published-looking word", "https://forge.example/user1alice", true},
		{"url-encoded", "Al%69ce", true},
		{"one-letter login", "user1 x", true},
		{"one-letter login inside a published value is not a leak", "ID_000001", false},
		{"a timestamp is published text", "2026-09-08T10:00:00Z", false},
		{"a vocabulary word is not a leak", "https://forge.example/owner1/repo1/issues/1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rules := Rules{Paths: map[string]Rule{"field": As(Redact)}, Vocabulary: testRules().Vocabulary}
			err := checkLeaks(rules, map[string]any{"field": tc.out}, secrets, published)
			if (err != nil) != tc.leak {
				t.Fatalf("leak = %v, want %v", err, tc.leak)
			}
			if err != nil && (!strings.Contains(err.Error(), "field") || strings.Contains(strings.ToLower(err.Error()), "alice")) {
				t.Errorf("a leak names the path and kind only: %v", err)
			}
		})
	}
}

// A fragment of a title surviving elsewhere is as much a leak as the title.
func Test_RedactedTextLeavesItsWordsAsSecrets(t *testing.T) {
	m := &Mapper{table: map[string]string{}, counters: map[string]int{}}
	m.redact(As(Redact), "Secret launch-plan v2 2026")
	var got []string
	for _, s := range m.secrets {
		got = append(got, s.value)
	}
	// short words and bare numbers would match published text by chance
	if diff := cmp.Diff([]string{"Secret", "launch", "plan", "Secret launch-plan v2 2026"}, got); diff != "" {
		t.Errorf("secrets (-want +got):\n%s", diff)
	}
}

// A rewrite that passes a part through unchanged is the bug the leak check
// exists for.
func Test_LeakingRewriteIsRefused(t *testing.T) {
	r := testRules()
	r.Paths["items[].url"] = Rule{Class: Func, Rewrite: func(m *Mapper, raw string) (string, error) {
		m.Secret("acme", "owner")
		return raw, nil
	}}
	_, err := Anonymise(r, []byte(`{"items": [{"url": "https://forge.example/acme/rocket"}]}`))
	if err == nil || strings.Contains(err.Error(), "acme") {
		t.Fatalf("want a refusal naming no value, got %v", err)
	}
}

// A token never belongs in a body, but if one ever got there it must not be
// published, whichever rule let it through.
func Test_TokensAreRefusedEvenInConstants(t *testing.T) {
	for _, marker := range []string{"ghp_abc", "github_pat_abc", "glpat-abc", "Bearer abc"} {
		r := testRules()
		r.Paths["requests[].query"] = ConstantOf(testQuery, marker)
		if _, err := Anonymise(r, []byte(`{"requests": [{"query": "`+marker+`"}]}`)); err == nil {
			t.Errorf("%q was published", marker)
		}
	}
}

func Test_CheckFormsAcceptsAnonymisedOutput(t *testing.T) {
	out, err := Anonymise(testRules(), []byte(`{
		"pollTime": "2026-09-08T10:00:00Z",
		"requests": [{"query": "`+testQuery+`", "variables": {"q": "state:open involves:realme", "id": "N_a", "after": "abc"}}],
		"items": [{"id": "N_a", "title": "x", "url": "https://forge.example/a/b/issues/7#note-9", "author": "helper[bot]",
		         "state": "OPEN", "sha": "0123456789abcdef0123456789abcdef01234567", "cursor": "abc", "additions": 1,
		         "repo": {"owner": "a", "name": "b"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckForms(testRules(), out); err != nil {
		t.Fatalf("anonymised output must pass the forms check: %v", err)
	}
}

func Test_CheckFormsRefusesAnythingElse(t *testing.T) {
	for _, tc := range []struct{ path, value string }{
		{"items[].title", "Fix the thing"},
		{"items[].author", "alice"},
		{"items[].author", "user1x"},
		{"items[].url", "https://forge.example/acme/rocket/issues/1"},
		{"requests[].variables.q", "state:open involves:alice"},
		{"items[].sha", "0123456789abcdef0123456789abcdef01234567"},
		{"items[].title", "ghp_abc"},
		// an enum value is allowed at the paths whose rule lists it, nowhere else
		{"items[].title", "OPEN"},
		{"items[].unlisted", "redacted-1"},
	} {
		key := strings.TrimPrefix(tc.path, "items[].")
		var doc []byte
		if strings.HasPrefix(tc.path, "items[].") {
			doc, _ = json.Marshal(map[string]any{"items": []any{map[string]any{key: tc.value}}})
		} else {
			doc, _ = json.Marshal(map[string]any{"requests": []any{map[string]any{"variables": map[string]any{"q": tc.value}}}})
		}
		err := CheckForms(testRules(), doc)
		if err == nil {
			t.Errorf("%s = %q passed the forms check", tc.path, tc.value)
			continue
		}
		if !strings.Contains(err.Error(), tc.path) {
			t.Errorf("the refusal must name the path %s: %v", tc.path, err)
		}
	}
}

func Test_CounterPatternCoversFixedValuesAndSuffixes(t *testing.T) {
	form := CounterForm(login)
	for _, s := range []string{"user1", "user12[bot]", "me", "ci-bot"} {
		if !form.MatchString(s) {
			t.Errorf("%q should be a published login", s)
		}
	}
	if slices.ContainsFunc([]string{"alice", "user", "me2"}, form.MatchString) {
		t.Error("a raw login passed as published")
	}
}
