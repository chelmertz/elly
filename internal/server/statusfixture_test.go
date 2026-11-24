package server

import (
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// storedAt varies per run; the fixture holds a fixed time in its place.
var storedAt = regexp.MustCompile(`"stored_at":"[^"]*"`)

var updateStatus = flag.Bool("update-status", false, "rewrite testdata/config-status.txt from the handler")

// The bodies GET /api/v0/config/status sends, configured first and setup
// mode second, one per line. dotfiles' bin/prom-system-health reads them:
// its credential probe greps the body, and its test (bin/tests/elly-status)
// runs that grep against a copy of this file. A change here that the copy
// does not get is a false CredentialExpired alert there.
func TestConfigStatusFixture(t *testing.T) {
	setup := newRoutes(t, "")
	setupBody := setup.do("GET", "/api/v0/config/status", "").Body.String()
	configured := newRoutes(t, "")
	if err := configured.store.StorePAT("github_pat_main", "me", time.Time{}); err != nil {
		t.Fatal(err)
	}
	configuredBody := configured.do("GET", "/api/v0/config/status", "").Body.String()
	configuredBody = storedAt.ReplaceAllString(configuredBody, `"stored_at":"2026-01-01T00:00:00Z"`)
	got := strings.TrimSpace(configuredBody) + "\n" + strings.TrimSpace(setupBody) + "\n"

	path := filepath.Join("testdata", "config-status.txt")
	if *updateStatus {
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path) //nolint:gosec // the test's own fixture
	if err != nil {
		t.Fatalf("%v; run go test ./internal/server -run TestConfigStatusFixture -update-status, then copy it to dotfiles' bin/tests/elly-config-status.txt", err)
	}
	if got != string(want) {
		t.Errorf("the status bodies changed; rerun with -update-status and copy the file to dotfiles' bin/tests/elly-config-status.txt:\n%s", got)
	}
}
