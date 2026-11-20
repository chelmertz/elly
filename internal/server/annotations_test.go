package server

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/chelmertz/elly/internal/storage"
	"github.com/chelmertz/elly/internal/types"
)

var testNow = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

func body(expires string, extra string) string {
	return `{"state":"needs_you","note":"look","links":[{"label":"Approve","url":"claude-review://o/r/1/approve"}],"expiresAt":"` + expires + `"` + extra + `}`
}

func TestValidateAnnotation(t *testing.T) {
	in15 := testNow.Add(15 * time.Minute).Format(time.RFC3339)
	schemes := []string{"claude-review"}
	cases := []struct {
		name    string
		source  string
		body    string
		schemes []string
		ok      bool
	}{
		{"valid", "claude-review", body(in15, ""), schemes, true},
		{"https link needs no flag", "t", `{"state":"handled","links":[{"label":"x","url":"https://github.com/o/r/pull/1"}],"expiresAt":"` + in15 + `"}`, nil, true},
		{"custom scheme without the flag", "t", body(in15, ""), nil, false},
		{"javascript link", "t", `{"state":"handled","links":[{"label":"x","url":"javascript:alert(1)"}],"expiresAt":"` + in15 + `"}`, schemes, false},
		{"http link", "t", `{"state":"handled","links":[{"label":"x","url":"http://example.com"}],"expiresAt":"` + in15 + `"}`, schemes, false},
		{"source with capitals", "Tool", body(in15, ""), schemes, false},
		{"source too long", strings.Repeat("a", 41), body(in15, ""), schemes, false},
		{"running", "claude-review", strings.Replace(body(in15, ""), "needs_you", "running", 1), schemes, true},
		{"stuck", "claude-review", strings.Replace(body(in15, ""), "needs_you", "stuck", 1), schemes, true},
		{"note at the limit", "t", strings.Replace(body(in15, ""), `"look"`, `"`+strings.Repeat("ö", 300)+`"`, 1), schemes, true},
		{"unknown state", "t", strings.Replace(body(in15, ""), "needs_you", "done", 1), schemes, false},
		{"unknown field", "t", body(in15, `,"extra":1`), schemes, false},
		{"unknown link field", "t", strings.Replace(body(in15, ""), `"label"`, `"title":"x","label"`, 1), schemes, false},
		{"note too long", "t", strings.Replace(body(in15, ""), `"look"`, `"`+strings.Repeat("n", 301)+`"`, 1), schemes, false},
		{"note with a control character", "t", strings.Replace(body(in15, ""), `"look"`, `"a\u0007b"`, 1), schemes, false},
		{"label too long", "t", strings.Replace(body(in15, ""), `"Approve"`, `"`+strings.Repeat("l", 41)+`"`, 1), schemes, false},
		{"five links", "t", `{"state":"handled","links":[` + strings.Repeat(`{"label":"x","url":"https://a.example"},`, 4) + `{"label":"x","url":"https://a.example"}],"expiresAt":"` + in15 + `"}`, schemes, false},
		{"four links", "t", `{"state":"handled","links":[` + strings.Repeat(`{"label":"x","url":"https://a.example"},`, 3) + `{"label":"x","url":"https://a.example"}],"expiresAt":"` + in15 + `"}`, schemes, true},
		{"link at the length limit", "t", `{"state":"handled","links":[{"label":"x","url":"https://a.example/` + strings.Repeat("a", 2048-len("https://a.example/")) + `"}],"expiresAt":"` + in15 + `"}`, schemes, true},
		{"link over the length limit", "t", `{"state":"handled","links":[{"label":"x","url":"https://a.example/` + strings.Repeat("a", 2049-len("https://a.example/")) + `"}],"expiresAt":"` + in15 + `"}`, schemes, false},
		{"expires in exactly a minute", "t", body(testNow.Add(time.Minute).Format(time.RFC3339), ""), schemes, true},
		{"expires in exactly 24 hours", "t", body(testNow.Add(24*time.Hour).Format(time.RFC3339), ""), schemes, true},
		{"expires in 30 seconds", "t", body(testNow.Add(30*time.Second).Format(time.RFC3339), ""), schemes, false},
		{"expires in 25 hours", "t", body(testNow.Add(25*time.Hour).Format(time.RFC3339), ""), schemes, false},
		{"expiry with an offset", "t", body(testNow.Add(time.Hour).In(time.FixedZone("CET", 3600)).Format(time.RFC3339), ""), schemes, true},
		{"expiry not RFC 3339", "t", body("tomorrow", ""), schemes, false},
		{"one primary link", "t", `{"state":"info","links":[{"label":"Start","url":"claude-review://o/r/1?from=elly","primary":true},{"label":"x","url":"https://a.example"}],"expiresAt":"` + in15 + `"}`, schemes, true},
		{"two primary links", "t", `{"state":"info","links":[{"label":"Start","url":"claude-review://o/r/1","primary":true},{"label":"x","url":"https://a.example","primary":true}],"expiresAt":"` + in15 + `"}`, schemes, false},
		{"an action from claude-review", "claude-review", action("approve", "claude-review://o/r/1/approve?at="+strings.Repeat("a", 40)+"&token="+strings.Repeat("b", 48)), schemes, true},
		{"open is the attach verb", "claude-review", action("open", "claude-review://o/r/1/attach"), schemes, true},
		{"start is the bare link", "claude-review", action("start", "claude-review://o/r/1?from=elly"), schemes, true},
		{"help is the states verb", "claude-review", action("help", "claude-review://o/r/1/states"), schemes, true},
		{"an action from another source", "t", action("approve", "claude-review://o/r/1/approve"), schemes, false},
		{"an action on an https link", "claude-review", action("approve", "https://github.com/o/r/pull/1"), schemes, false},
		{"an action on another verb", "claude-review", action("approve", "claude-review://o/r/1/attach"), schemes, false},
		{"an action on another PR", "claude-review", action("open", "claude-review://o/r/2/attach"), schemes, false},
		{"an unknown action", "claude-review", action("merge", "claude-review://o/r/1/merge"), schemes, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := validateAnnotation(c.source, testPrUrl, strings.NewReader(c.body), c.schemes, testNow)
			if (err == nil) != c.ok {
				t.Errorf("err = %v, want ok=%v", err, c.ok)
			}
		})
	}
}

func TestValidateAnnotationKeepsExpiresAtAsWritten(t *testing.T) {
	expires := testNow.Add(time.Hour).In(time.FixedZone("CET", 3600)).Format(time.RFC3339)
	a, at, err := validateAnnotation("t", testPrUrl, strings.NewReader(body(expires, "")), []string{"claude-review"}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if a.ExpiresAt != expires || !at.Equal(testNow.Add(time.Hour)) {
		t.Errorf("got %q / %v", a.ExpiresAt, at)
	}
}

func TestAnnotationHandlers(t *testing.T) {
	store := storage.NewStorage(slog.New(slog.NewTextHandler(io.Discard, nil)), filepath.Join(t.TempDir(), "elly.db"))
	const prUrl = "https://github.com/o/r/pull/1"
	if err := store.StoreRepoPrs([]types.ViewPr{{Url: prUrl, LastUpdated: time.Now(), RawJsonResponse: []byte("{}")}}); err != nil {
		t.Fatal(err)
	}
	cfg := HttpServerConfig{Store: store, AnnotationToken: "secret", AnnotationSchemes: []string{"claude-review"}}
	mux := http.NewServeMux()
	registerAnnotationRoutes(mux, cfg)
	in15 := time.Now().Add(15 * time.Minute).Format(time.RFC3339)

	do := func(method, url, token, b string) int {
		r := httptest.NewRequest(method, url, strings.NewReader(b))
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w.Code
	}
	path := "/api/v0/prs/" + base64.StdEncoding.EncodeToString([]byte(prUrl)) + "/annotations/claude-review"
	unknown := "/api/v0/prs/" + base64.StdEncoding.EncodeToString([]byte("https://github.com/o/r/pull/2")) + "/annotations/claude-review"

	if got := do("PUT", path, "", body(in15, "")); got != http.StatusUnauthorized {
		t.Errorf("no token: %d", got)
	}
	if got := do("PUT", path, "wrong", body(in15, "")); got != http.StatusUnauthorized {
		t.Errorf("wrong token: %d", got)
	}
	if got := do("PUT", unknown, "secret", body(in15, "")); got != http.StatusNotFound {
		t.Errorf("PR elly does not list: %d", got)
	}
	if got := do("PUT", path, "secret", body("soon", "")); got != http.StatusBadRequest {
		t.Errorf("invalid body: %d", got)
	}
	if got := do("PUT", path, "secret", strings.Repeat(" ", 4097)+body(in15, "")); got != http.StatusRequestEntityTooLarge {
		t.Errorf("body over 4 KiB: %d", got)
	}
	if got := do("PUT", path, "secret", body(in15, "")); got != http.StatusNoContent {
		t.Fatalf("valid write: %d", got)
	}
	if got := store.Prs().Prs[0].Annotations; len(got) != 1 || got[0].Source != "claude-review" {
		t.Fatalf("stored: %+v", got)
	}
	if got := do("DELETE", path, "", ""); got != http.StatusUnauthorized {
		t.Errorf("delete without token: %d", got)
	}
	if got := do("DELETE", path, "secret", ""); got != http.StatusNoContent {
		t.Errorf("delete: %d", got)
	}
	if got := store.Prs().Prs[0].Annotations; len(got) != 0 {
		t.Errorf("after delete: %+v", got)
	}

	cfg.AnnotationToken = ""
	mux = http.NewServeMux()
	registerAnnotationRoutes(mux, cfg)
	if got := do("PUT", path, "", body(in15, "")); got != http.StatusServiceUnavailable {
		t.Errorf("no token configured: %d, want writes disabled", got)
	}
}

func TestWriteAnnotationToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "elly", "annotation-token")
	tok, err := WriteAnnotationToken(path)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != tok || len(tok) < 32 {
		t.Fatalf("token file %q, token %q, err %v", b, tok, err)
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("token file mode %v, want 0600", fi.Mode().Perm())
	}
	again, _ := WriteAnnotationToken(path)
	if again == tok {
		t.Error("token did not rotate on restart")
	}
}

// A bidi override or a zero-width character makes a note or a label read as
// something else than it says ("approve" shown, "evorppa" stored), so format
// characters are refused with the control characters.
func TestPlainTextRefusesFormatCharacters(t *testing.T) {
	for _, s := range []string{"a\u202eb", "a\u200bb", "a\u2066b", "\ufeffa", "a\u200db"} {
		if err := plainText(s, 100); err == nil {
			t.Errorf("%q accepted", s)
		}
	}
	if err := plainText("approve · ready ✓ é", 100); err != nil {
		t.Errorf("ordinary text refused: %v", err)
	}
}

// A refused body may hold a live Approve token; elly's error must not carry
// it back to the writer's logs. Tokens are hex, which %q, JSON and URL
// encoding leave as they are, so the raw value covers every form.
func TestRefusedAnnotationNeverEchoesAToken(t *testing.T) {
	const token = "9f86d081884c7d659a2feaa0c55ad015"
	in15 := testNow.Add(15 * time.Minute).Format(time.RFC3339)
	link := `{"label":"Approve","url":"claude-review://o/r/1/approve?token=` + token + `"}`
	cases := map[string]string{
		"scheme not allowed":  `{"state":"needs_you","links":[` + link + `],"expiresAt":"` + in15 + `"}`,
		"second link refused": `{"state":"needs_you","links":[{"label":"x","url":"https://a.example"},` + strings.Replace(link, "claude-review:", "evil:", 1) + `],"expiresAt":"` + in15 + `"}`,
		"unknown field":       `{"state":"needs_you","links":[` + link + `],"extra":"` + token + `","expiresAt":"` + in15 + `"}`,
		"wrong-typed state":   `{"state":1,"links":[` + link + `],"expiresAt":"` + in15 + `"}`,
		"note with token":     `{"state":"needs_you","note":"` + strings.Repeat("n", 301) + token + `","expiresAt":"` + in15 + `"}`,
		"truncated":           `{"state":"needs_you","links":[` + link,
	}
	for name, b := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, err := validateAnnotation("claude-review", testPrUrl, strings.NewReader(b), nil, testNow)
			if err == nil {
				t.Fatal("accepted")
			}
			if strings.Contains(err.Error(), token) {
				t.Errorf("error carries the token: %v", err)
			}
		})
	}
	_, _, err := validateAnnotation("claude-review", testPrUrl, strings.NewReader(cases["second link refused"]), []string{"claude-review"}, testNow)
	if err == nil || !strings.HasPrefix(err.Error(), "links[1]: ") {
		t.Errorf("err = %v, want it to name links[1]", err)
	}
}

func TestTokensAreHex(t *testing.T) {
	// The no-echo tests rely on encodings leaving hex alone.
	for _, r := range "9f86d081884c7d659a2feaa0c55ad015" {
		if !strings.ContainsRune("0123456789abcdef", r) {
			t.Fatalf("%q is not hex", r)
		}
	}
}

// testPrUrl is the PR the validation cases annotate.
const testPrUrl = "https://github.com/o/r/pull/1"

// action is a needs_you body with one link naming its action.
func action(name, url string) string {
	return `{"state":"needs_you","links":[{"label":"x","url":"` + url + `","action":"` + name + `"}],"expiresAt":"` + testNow.Add(15*time.Minute).Format(time.RFC3339) + `"}`
}

// claudeReviewRows is the fixture claude-review generates from its table
// (its testdata/rows.json); the rules elly enforces must be exactly it.
func TestClaudeReviewRowsMatchTheFixture(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "claude-review-rows.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture map[string]rowRule
	if err := json.Unmarshal(b, &fixture); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fixture, claudeReviewRows) {
		t.Errorf("claudeReviewRows %v, fixture %v", claudeReviewRows, fixture)
	}
	// The security-relevant rules, asserted here so a widened fixture
	// cannot widen them unreviewed.
	for state, r := range claudeReviewRows {
		if slices.Contains(r.Offered, types.ActionApprove) != (state == types.AnnotationNeedsYou) {
			t.Errorf("approve on %s: %v", state, r.Offered)
		}
		if slices.Contains(r.Offered, types.ActionOpen) != slices.Contains([]string{types.AnnotationRunning, types.AnnotationNeedsYou, types.AnnotationStuck, types.AnnotationHandled}, state) {
			t.Errorf("open on %s: %v", state, r.Offered)
		}
		if len(r.Primary) > 0 && !slices.Contains([]string{types.AnnotationInfo, types.AnnotationNeedsYou, types.AnnotationStuck}, state) {
			t.Errorf("a primary on %s", state)
		}
	}
}

// A claude-review row whose links break its state's rules is refused.
func TestValidateAnnotationChecksClaudeReviewRows(t *testing.T) {
	exp := testNow.Add(15 * time.Minute).Format(time.RFC3339)
	for name, c := range map[string]struct {
		body string
		ok   bool
	}{
		"approve on needs_you": {`{"state":"needs_you","links":[{"label":"Approve","url":"claude-review://o/r/1/approve?at=` + strings.Repeat("a", 40) + `&token=` + strings.Repeat("b", 48) + `","action":"approve","primary":true}],"expiresAt":"` + exp + `"}`, true},
		"approve on handled":   {`{"state":"handled","links":[{"label":"Approve","url":"claude-review://o/r/1/approve?at=` + strings.Repeat("a", 40) + `&token=` + strings.Repeat("b", 48) + `","action":"approve"}],"expiresAt":"` + exp + `"}`, false},
		"a primary on running": {`{"state":"running","links":[{"label":"Open Claude","url":"claude-review://o/r/1/attach","action":"open","primary":true}],"expiresAt":"` + exp + `"}`, false},
		"open on running":      {`{"state":"running","links":[{"label":"Open Claude","url":"claude-review://o/r/1/attach","action":"open"}],"expiresAt":"` + exp + `"}`, true},
		"start on handled":     {`{"state":"handled","links":[{"label":"Start","url":"claude-review://o/r/1?from=elly","action":"start"}],"expiresAt":"` + exp + `"}`, false},
	} {
		_, _, err := validateAnnotation("claude-review", testPrUrl, strings.NewReader(c.body), []string{"claude-review"}, testNow)
		if (err == nil) != c.ok {
			t.Errorf("%s: err %v, want ok=%v", name, err, c.ok)
		}
	}
}
