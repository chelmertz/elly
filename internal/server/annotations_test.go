package server

import (
	"encoding/base64"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chelmertz/elly/internal/points"
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
		{"unknown state", "t", strings.Replace(body(in15, ""), "needs_you", "done", 1), schemes, false},
		{"unknown field", "t", body(in15, `,"extra":1`), schemes, false},
		{"unknown link field", "t", strings.Replace(body(in15, ""), `"label"`, `"title":"x","label"`, 1), schemes, false},
		{"note too long", "t", strings.Replace(body(in15, ""), `"look"`, `"`+strings.Repeat("n", 201)+`"`, 1), schemes, false},
		{"note with a control character", "t", strings.Replace(body(in15, ""), `"look"`, `"a\u0007b"`, 1), schemes, false},
		{"label too long", "t", strings.Replace(body(in15, ""), `"Approve"`, `"`+strings.Repeat("l", 41)+`"`, 1), schemes, false},
		{"five links", "t", `{"state":"handled","links":[` + strings.Repeat(`{"label":"x","url":"https://a.example"},`, 4) + `{"label":"x","url":"https://a.example"}],"expiresAt":"` + in15 + `"}`, schemes, false},
		{"expires in 30 seconds", "t", body(testNow.Add(30*time.Second).Format(time.RFC3339), ""), schemes, false},
		{"expires in 25 hours", "t", body(testNow.Add(25*time.Hour).Format(time.RFC3339), ""), schemes, false},
		{"expiry with an offset", "t", body(testNow.Add(time.Hour).In(time.FixedZone("CET", 3600)).Format(time.RFC3339), ""), schemes, true},
		{"expiry not RFC 3339", "t", body("tomorrow", ""), schemes, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := validateAnnotation(c.source, strings.NewReader(c.body), c.schemes, testNow)
			if (err == nil) != c.ok {
				t.Errorf("err = %v, want ok=%v", err, c.ok)
			}
		})
	}
}

func TestValidateAnnotationKeepsExpiresAtAsWritten(t *testing.T) {
	expires := testNow.Add(time.Hour).In(time.FixedZone("CET", 3600)).Format(time.RFC3339)
	a, at, err := validateAnnotation("t", strings.NewReader(body(expires, "")), []string{"claude-review"}, testNow)
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

func TestIndexRendersAnnotationLinks(t *testing.T) {
	render := func(schemes []string) string {
		temp, err := parseIndex(schemes)
		if err != nil {
			t.Fatal(err)
		}
		const u = "https://github.com/o/r/pull/1"
		data := IndexHtmlData{
			Prs: []types.ViewPr{{Url: u, Annotations: []types.Annotation{{
				Source: "tool", State: types.AnnotationNeedsYou, Note: "<b>safe to merge</b>",
				Links: []types.AnnotationLink{{Label: "Open session", Url: "claude-review://o/r/1/approve?at=abc"}},
			}}}},
			PointsPerPrUrl: map[string]*points.Points{u: {Total: 1}},
		}
		var out strings.Builder
		if err := temp.Execute(&out, data); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}

	page := render([]string{"claude-review"})
	if !strings.Contains(page, `href="claude-review://o/r/1/approve?at=abc"`) {
		t.Error("allowlisted custom-scheme link not rendered as a link")
	}
	if !strings.Contains(page, "claude-review · approve") {
		t.Error("the link's verb is not shown beside its label")
	}
	if strings.Contains(page, "<b>safe to merge</b>") {
		t.Error("note rendered unescaped")
	}

	if page := render(nil); strings.Contains(page, "claude-review://o/r/1/approve") && strings.Contains(page, `href="claude-review:`) {
		t.Error("a scheme no longer allowlisted still renders as a link")
	}
}
