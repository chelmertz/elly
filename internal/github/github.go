package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"time"

	"log/slog"

	"github.com/chelmertz/elly/internal/types"
)

var ErrClient = errors.New("github returned client error")
var ErrGithubServer = errors.New("github returned server error")

type ErrRateLimited struct {
	NextAllowedAfter time.Duration
}

func (e *ErrRateLimited) Error() string {
	return fmt.Sprintf("%v: rate limited, next allowed after %s", ErrClient, e.NextAllowedAfter)
}

type querySearchPrsInvolvingMeGraphQl struct {
	Data struct {
		Search struct {
			Edges []struct {
				Node json.RawMessage
			}
		}
	}
}

type prSearchResultGraphQl struct {
	Url            string
	Title          string
	IsDraft        bool
	ReviewRequests struct {
		Nodes []struct {
			RequestedReviewer struct {
				Login string
			}
		}
	}
	ReviewDecision string
	UpdatedAt      string
	Author         struct {
		Login string
	}

	Repository struct {
		Url   string
		Name  string
		Owner struct {
			Login string
		}
	}

	Additions int
	Deletions int
	Comments  struct {
		Edges []struct {
			Node struct {
				UpdatedAt string
				Author    struct {
					Login string
				}
				Url       string
				Body      string
				Reactions prReviewThreadCommentReactionGraphQl
			}
		}
	}
	Commits struct {
		Nodes []struct {
			Commit struct {
				Author struct {
					Date  string
					Email string
					Name  string
				}
			}
		}
	}
	ReviewThreads struct {
		Edges []struct {
			Node prReviewThreadGraphQl
		}
	}
	Reviews struct {
		Edges []struct {
			Node struct {
				Author struct {
					Login string
				}
				Url   string
				Body  string
				State string
			}
		}
	}
}

type prReviewThreadGraphQl struct {
	IsResolved  bool
	IsOutdated  bool
	IsCollapsed bool
	Comments    struct {
		Nodes []prReviewThreadCommentGraphQl
	}
}

type prReviewThreadCommentGraphQl struct {
	Author struct {
		Login string
	}
	Body      string
	Url       string
	Reactions prReviewThreadCommentReactionGraphQl
}

type prReviewThreadCommentReactionGraphQl struct {
	Edges []struct {
		Node struct {
			Content string
			User    struct {
				Login string
			}
		}
	}
}

func checkExpiration(expiration string, logger *slog.Logger) {
	expires, err := time.Parse("2006-01-02 15:04:05 -0700", expiration)
	if err != nil {
		logger.Error("could not parse github token expiration", slog.Any("error", err), slog.String("expiration", expiration))
	} else if expires.Before(time.Now().Add(10 * 24 * time.Hour)) {
		// less than 10 days left on token, warn!
		logger.Warn("github token expires soon", slog.Time("expires", expires), slog.Int("days_left", int(time.Until(expires).Hours()/24)))
	}
}

func graphqlRequest(query, token string, logger *slog.Logger) ([]byte, error) {
	payload := struct {
		Query string `json:"query"`
	}{
		Query: query,
	}
	jsonBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("could not marshal graphql json: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
	defer cancel()

	httpClient := &http.Client{}

	request, err := http.NewRequestWithContext(ctx, "POST", "https://api.github.com/graphql", bytes.NewReader(jsonBytes))
	request.Header.Add("Authorization", "bearer "+token)
	if err != nil {
		return nil, fmt.Errorf("could not construct github request: %w", err)
	}

	logger.Debug("querying github api")
	response, err := httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("could not request github: %w", err)
	}
	defer response.Body.Close()

	if expiration := response.Header.Get("Github-Authentication-Token-Expiration"); expiration != "" {
		checkExpiration(expiration, logger)
	}

	respBody, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, fmt.Errorf("could not read github username response: %w", err)
	}

	// since graphql returns 200 but still possibly errors, we need to check for
	// those somewhere, and it seems more proper to do it close to the actual request
	var errorResponse struct {
		Errors []struct {
			Type    string
			Message string
		}
	}
	jsonErr := json.Unmarshal(respBody, &errorResponse)
	if jsonErr != nil {
		return nil, fmt.Errorf("%v: json unmarshal error", ErrClient)
	}
	if len(errorResponse.Errors) > 0 {
		for _, e := range errorResponse.Errors {
			if e.Type == "RATE_LIMITED" {
				// There are a lot of complexity about "points" and trying to
				// estimate what the "cost" is, which is too hard for me to
				// grasp on this page:
				// https://docs.github.com/en/graphql/overview/rate-limits-and-query-limits-for-the-graphql-api#exceeding-the-rate-limit
				// but the specific advice on listening to the x-ratelimit-reset
				// header seems easy to follow (but they just had to complement
				// it with a retry-after header as well, so... look for both of
				// those)

				// example of response found in logs (note that there are more
				// properties than `error` that might look like a success
				// (seriously, why just not speak HTTP and status codes?), but
				// we need to verify the error case like this:
				//
				// {"errors":[{"type":"RATE_LIMITED","message":"API rate limit exceeded for user ID 213477."}]}
				logger.Error("github rate limited", slog.Any("response_body_graphql_errors", errorResponse.Errors), slog.Any("response_headers", response.Header))
				// TODO after examining the logs, refactor to use ErrRateLimited
				return nil, fmt.Errorf("%v: rate limit", ErrClient)
			}
		}
	}

	logger.Info("github response", slog.String("body", string(respBody)), slog.Int("status", response.StatusCode))

	if response.StatusCode >= 400 {
		logger.Warn("response", slog.Int("response_code", response.StatusCode), slog.String("body", string(respBody)))
		if response.StatusCode < 500 {
			return nil, fmt.Errorf("%v: github response code %d", ErrClient, response.StatusCode)
		}
		return nil, fmt.Errorf("%v: github response code %d", ErrGithubServer, response.StatusCode)
	}
	return respBody, nil
}

// UsernameFromPat() will return the username for the given personal access
// token, to avoid having to provide the username explicitly.
func UsernameFromPat(token string, logger *slog.Logger) (string, error) {
	respBody, err := graphqlRequest(`query { viewer { login } }`, token, logger)
	if err != nil {
		return "", fmt.Errorf("could not query github for username: %w", err)
	}

	var typedResponse struct {
		Data struct {
			Viewer struct {
				Login string
			}
		}
	}
	err = json.Unmarshal(respBody, &typedResponse)
	if err != nil {
		return "", fmt.Errorf("could not unmarshal github username response: %w", err)
	}

	return typedResponse.Data.Viewer.Login, nil
}

var ignoredLastPrCommenters = []string{"github-actions", "vercel"}

func QueryGithub(token string, username string, logger *slog.Logger) ([]types.ViewPr, error) {
	respBody, err := graphqlRequest(querySearchPrsInvolvingUser(username), token, logger)
	if err != nil {
		return nil, fmt.Errorf("could not query github for PRs: %v", err)
	}

	// Using json.RawMessage for the response, so that we can store the raw
	// JSON (not the parsed response) of each PR, for debugging reasons.
	// Debugging > efficiency, in this case.
	var rawResponse querySearchPrsInvolvingMeGraphQl
	err = json.Unmarshal(respBody, &rawResponse)
	if err != nil {
		return nil, fmt.Errorf("could not unmarshal github response: %w", err)
	}

	viewPrs := make([]types.ViewPr, 0)

	for _, prEdge := range rawResponse.Data.Search.Edges {
		var pr prSearchResultGraphQl
		err = json.Unmarshal(prEdge.Node, &pr)
		if err != nil {
			return nil, fmt.Errorf("could not re-marshal github PR, to store raw json for debugging (url=%s): %w", pr.Url, err)
		}
		reviewStatus := pr.ReviewDecision

		updatedAt, err := time.Parse(time.RFC3339, pr.UpdatedAt)
		if err != nil {
			// not really a fatal error, just log it
			logger.Warn("could not parse time", slog.String("updatedAt", pr.UpdatedAt), slog.String("pr_url", pr.Url))
			updatedAt = time.Time{}
		}

		lastPrCommenter := ""
		for _, c := range pr.Comments.Edges {
			if slices.Contains(ignoredLastPrCommenters, c.Node.Author.Login) {
				continue
			}
			lastPrCommenter = c.Node.Author.Login
		}

		threadsActionable, threadsWaiting := actionableThreads(pr, username)

		reviewUsers := make([]string, 0)
		for _, u := range pr.ReviewRequests.Nodes {
			reviewUsers = append(reviewUsers, u.RequestedReviewer.Login)
		}

		for _, a := range pr.Reviews.Edges {
			// For some reason, the "Reviews" graph can contain a separate
			// approval that is _not_ registered as the ReviewDecision,
			// something that went unnoticed for ~5 months of using this API.
			//
			// Note that the general "reviewDecision" can be "CHANGES_REQUESTED"
			// which weighs higher. Only set "APPROVED" if the reviewDecision is
			// empty.
			if a.Node.State == "APPROVED" && reviewStatus == "" {
				reviewStatus = "APPROVED"
				break
			}
		}

		viewPr := types.ViewPr{
			ReviewStatus:             reviewStatus,
			Url:                      pr.Url,
			Title:                    pr.Title,
			Author:                   pr.Author.Login,
			RepoName:                 pr.Repository.Name,
			RepoOwner:                pr.Repository.Owner.Login,
			RepoUrl:                  pr.Repository.Url,
			IsDraft:                  pr.IsDraft,
			LastUpdated:              updatedAt,
			LastPrCommenter:          lastPrCommenter,
			ThreadsActionable:        threadsActionable,
			ThreadsWaiting:           threadsWaiting,
			Additions:                pr.Additions,
			Deletions:                pr.Deletions,
			ReviewRequestedFromUsers: reviewUsers,
			RawJsonResponse:          prEdge.Node,
		}
		logger.Debug("fetched a pr", slog.Any("pr", viewPr))
		viewPrs = append(viewPrs, viewPr)
	}

	return viewPrs, nil
}

func userReactedToComment(reactions prReviewThreadCommentReactionGraphQl, username string) bool {
	for _, r := range reactions.Edges {
		if r.Node.User.Login == username {
			return true
		}
	}
	return false
}

func someoneElseReactedToComment(reactions prReviewThreadCommentReactionGraphQl, username string) bool {
	for _, r := range reactions.Edges {
		if r.Node.User.Login != username {
			return true
		}
	}
	return false
}

func actionableThreads(pr prSearchResultGraphQl, myUsername string) (actionable int, waiting int) {
	ownPr := pr.Author.Login == myUsername
	for _, t := range pr.ReviewThreads.Edges {
		if t.Node.IsCollapsed || t.Node.IsOutdated || t.Node.IsResolved {
			continue
		}

		if len(t.Node.Comments.Nodes) == 0 {
			// the types say this is possible, I haven't seen it in the wild though
			continue
		}

		lastComment := t.Node.Comments.Nodes[len(t.Node.Comments.Nodes)-1]
		lastCommenter := lastComment.Author.Login
		iCommentedLast := lastCommenter == myUsername
		iReactedToLastComment := userReactedToComment(lastComment.Reactions, myUsername)
		someoneElseReactedMyLastComment := iCommentedLast && someoneElseReactedToComment(lastComment.Reactions, myUsername)

		if ownPr && !iCommentedLast && !iReactedToLastComment {
			// someone else commented last, and this is our pr, and we haven't
			// acknowledged it yet with a reaction (emoji)
			actionable++
			continue
		}

		if ownPr && someoneElseReactedMyLastComment {
			// we commented last, and this is our pr, and they reacted to it
			// (thumbs up etc.) acknowledged it yet
			actionable++
			continue
		}

		if !ownPr && lastCommenter == myUsername {
			// we have the currently last word, the owner should reply or resolve the thread
			waiting++
			continue
		}

		threadStarter := t.Node.Comments.Nodes[0].Author.Login
		if threadStarter == myUsername && !iCommentedLast && !iReactedToLastComment {
			// we started the thread, and it's still open (and someone else has
			// the last word), and we haven't acknowledged it yet with a
			// reaction (emoji)
			actionable++
			continue
		}

		// not recorded so far: someone else started the thread, we
		// commented in the middle and someone else has the last word
	}

	return
}

func querySearchPrsInvolvingUser(username string) string {
	// the amount of nodes given in "first: x", etc. needs to be a bit
	// calibrated - if everything is too high, github will complain with a
	// MAX_NODE_LIMIT_EXCEEDED error
	query := `query {
  search(type: ISSUE, query: "state:open involves:%s type:pr archived:false", first: 100) {
    edges {
      node {
        ... on PullRequest {
          title
          url
          isDraft
          reviewRequests(first: 100) {
            nodes {
              requestedReviewer {
                ... on User {
                  login
                }
              }
            }
          }
          repository {
            url
            name
            owner {
              login
            }
          }
          reviewDecision
          updatedAt
          author {
            login
          }
          additions
          deletions
          comments(last: 5) {
            edges {
              node {
                updatedAt
                author {
                  login
                }
                url
                body
                reactions(first: 7) {
                    edges {
                        node {
                            content
                            user {
                                login
                            }
                        }
                    }
                }
              }
            }
          }
          commits(last: 1) {
            nodes {
              commit {
                author {
                  date
                  email
                  name
                }
                status {
                  contexts {
                    state
                    context
                    description
                    createdAt
                    targetUrl
                  }
                }
              }
            }
          }
		  # sigh, here we can't filter on the isResolved status, so we need to overfetch (a lot, potentially)
          reviewThreads(first: 15) {
            edges {
              node {
                isResolved
                isOutdated
                isCollapsed
                comments(first: 30) {
                  nodes {
                    author {
                      login
                    }
                    body
                    url
                    reactions(first: 7) {
                        edges {
                            node {
                                content
                                user {
                                    login
                                }
                            }
                        }
                    }
                  }
                }
              }
            }
          }
          reviews(first: 20) {
            edges {
                node {
                    author {
                        login
                    }
                    body
                    url
                    state
                }
            }
          }
        }
      }
    }
  }
}`
	return fmt.Sprintf(query, username)
}
