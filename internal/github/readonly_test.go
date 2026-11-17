package github

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The setup dialog tells a new user that elly only reads: nothing is posted
// or changed. Every request here is a GraphQL query; a mutation in this
// package would make that sentence false (internal/server/next.html).
func TestEllyOnlyReadsGithub(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(strings.ToLower(string(body)), "mutation") {
			t.Errorf("%s mentions a mutation; the setup dialog says elly only reads", f)
		}
	}
}
