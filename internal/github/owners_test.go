package github

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// ownerServer answers the owner probe with probe, and every search with one
// PR in each of the given owners' repos.
func ownerServer(t *testing.T, probe string, owners ...string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "repositoryOwner") {
			_, _ = w.Write([]byte(probe))
			return
		}
		var edges []string
		for i, o := range owners {
			edges = append(edges, fmt.Sprintf(`{"node":{"id":"PR_%d","url":"https://github.com/%s/r/pull/%d","title":"t","author":{"login":"me"},
				"updatedAt":"2026-09-08T10:00:00Z","repository":{"url":"https://github.com/%s/r","name":"r","owner":{"login":"%s"}},
				"comments":{"edges":[]},"reviewThreads":{"totalCount":0,"pageInfo":{"hasNextPage":false},"edges":[]}}}`, i, o, i, o, o))
		}
		_, _ = w.Write([]byte(`{"data":{"search":{"edges":[` + strings.Join(edges, ",") + `]}}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestOwnerReach(t *testing.T) {
	cases := []struct {
		name  string
		probe string
		want  string
		err   error
	}{
		{"sees a private repo", `{"data":{"repositoryOwner":{"login":"Acme","repositories":{"totalCount":3}}}}`, "Acme", nil},
		// a token pending the org's approval, or whose resource owner is
		// the user, sees the org but none of its private repos
		{"sees only public repos", `{"data":{"repositoryOwner":{"login":"acme","repositories":{"totalCount":0}}}}`, "", ErrNoAccess},
		{"no such owner", `{"data":{"repositoryOwner":null},"errors":[{"type":"NOT_FOUND"}]}`, "", ErrNoAccess},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := OwnerReach(ownerServer(t, tc.probe).URL, "github_pat_x", "acme", quietLogger())
			if got != tc.want || !errors.Is(err, tc.err) {
				t.Fatalf("got %q, %v", got, err)
			}
		})
	}
}

func TestOwnerReachReportsA401AsAnInvalidToken(t *testing.T) {
	_, err := OwnerReach(fixedResponseServer(t, 401, `{"message":"Bad credentials"}`).URL, "github_pat_x", "acme", quietLogger())
	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("%v", err)
	}
}

func TestPollOwnerKeepsOnlyItsOwnersPRs(t *testing.T) {
	srv := ownerServer(t, `{"data":{"repositoryOwner":{"login":"acme","repositories":{"totalCount":1}}}}`, "ACME", "someone-public")
	prs, _, err := PollOwner(srv.URL, "github_pat_x", "me", "acme", quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	if len(prs) != 1 || prs[0].RepoOwner != "ACME" {
		t.Fatalf("%+v", prs)
	}
}

func TestPollOwnerStopsWhenTheOwnerIsOutOfReach(t *testing.T) {
	srv := ownerServer(t, `{"data":{"repositoryOwner":{"login":"acme","repositories":{"totalCount":0}}}}`, "acme")
	if prs, _, err := PollOwner(srv.URL, "github_pat_x", "me", "acme", quietLogger()); !errors.Is(err, ErrNoAccess) || prs != nil {
		t.Fatalf("%+v %v", prs, err)
	}
}
