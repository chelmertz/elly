package server

import (
	"testing"

	"github.com/chelmertz/elly/internal/github"
)

// A pasted PAT is checked against GitHub's API unless a test points it
// elsewhere.
func TestGithubURLDefaultsToGithubsAPI(t *testing.T) {
	if got := (HttpServerConfig{}).githubURL(); got != github.DefaultAPIURL {
		t.Errorf("unset: got %q, want %q", got, github.DefaultAPIURL)
	}
	if got := (HttpServerConfig{GithubURL: "http://127.0.0.1:1"}).githubURL(); got != "http://127.0.0.1:1" {
		t.Errorf("set: got %q", got)
	}
}
