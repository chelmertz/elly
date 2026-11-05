package github

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/chelmertz/elly/internal/types"
)

const ownerReachQuery = `query($owner: String!) {
  repositoryOwner(login: $owner) { login repositories(privacy: PRIVATE) { totalCount } }
}`

func queryOwnerReach(owner string) graphqlQuery {
	return graphqlQuery{Query: ownerReachQuery, Variables: map[string]any{"owner": owner}}
}

// OwnerReach returns owner's login as GitHub spells it, or ErrNoAccess when
// token sees none of its private repositories (internal ones count). It is
// asked before every owner poll, because a token that lost its access still
// answers the search, with nothing in it.
func OwnerReach(baseURL, token, owner string, logger *slog.Logger) (string, error) {
	body, err := graphqlRequest(baseURL, queryOwnerReach(owner), token, logger)
	if err != nil {
		return "", err
	}
	var resp struct {
		Data struct {
			RepositoryOwner *struct {
				Login        string `json:"login"`
				Repositories struct {
					TotalCount int `json:"totalCount"`
				} `json:"repositories"`
			} `json:"repositoryOwner"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("%w: could not parse the owner probe: %w", ErrClient, err)
	}
	o := resp.Data.RepositoryOwner
	if o == nil || o.Repositories.TotalCount == 0 {
		return "", ErrNoAccess
	}
	return o.Login, nil
}

// PollOwner polls with an owner token and keeps only owner's PRs: the token
// may also see public repos elsewhere, whose PRs the primary token answers.
func PollOwner(baseURL, token, username, owner string, logger *slog.Logger) ([]types.ViewPr, []types.Degradation, error) {
	if _, err := OwnerReach(baseURL, token, owner, logger); err != nil {
		return nil, nil, err
	}
	prs, ds, err := QueryGithub(baseURL, token, username, logger)
	if err != nil {
		return nil, nil, err
	}
	kept := make([]types.ViewPr, 0, len(prs))
	for _, pr := range prs {
		if strings.EqualFold(pr.RepoOwner, owner) {
			kept = append(kept, pr)
		}
	}
	return kept, ds, nil
}
