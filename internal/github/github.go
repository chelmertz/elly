package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"log/slog"

	"github.com/chelmertz/elly/internal/types"
)

var ErrClient = errors.New("github returned client error")
var ErrGithubServer = errors.New("github returned server error")

type querySearchPrsInvolvingMeGraphQl struct {
	Data struct {
		Search struct {
			Edges []struct {
				Node prSearchResultGraphQl
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

func QueryGithub(token string, username string, logger *slog.Logger) ([]types.ViewPr, error) {
	payload := struct {
		Query string `json:"query"`
	}{
		Query: querySearchPrsInvolvingUser(username),
	}
	jsonBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("could not marshal json: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
	defer cancel()

	httpClient := &http.Client{}

	request, err := http.NewRequestWithContext(ctx, "POST", "https://api.github.com/graphql", bytes.NewReader(jsonBytes))
	request.Header.Add("Authorization", "bearer "+token)
	if err != nil {
		return nil, fmt.Errorf("could not construct github request: %w", err)
	}

	logger.Info("querying github api")
	response, err := httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("could not request github: %w", err)
	}
	defer response.Body.Close()

	if expiration := response.Header.Get("Github-Authentication-Token-Expiration"); expiration != "" {
		expires, err := time.Parse("2006-01-02 15:04:05 -0700", expiration)
		if err != nil {
			logger.Error("could not parse github token expiration", err, slog.String("expiration", expiration))
		} else if expires.After(time.Now().Add(-1 * 24 * 10 * time.Hour)) {
			// less than 10 days left on token, warn!
			logger.Warn("github token expires soon", slog.Time("expires", expires), slog.Int("days_left", int(time.Until(expires).Hours()/24)))
		}
	}

	respBody, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, fmt.Errorf("could not read github response: %w", err)
	}

	if response.StatusCode >= 400 {
		logger.Warn("response", slog.Int("response_code", response.StatusCode), slog.String("body", string(respBody)))
		if response.StatusCode < 500 {
			return nil, ErrClient
		}
		return nil, ErrGithubServer
	}

	typedResponse := querySearchPrsInvolvingMeGraphQl{}
	err = json.Unmarshal(respBody, &typedResponse)
	if err != nil {
		return nil, fmt.Errorf("could not unmarshal github response: %w", err)
	}

	viewPrs := make([]types.ViewPr, 0)

	for _, prEdge := range typedResponse.Data.Search.Edges {
		pr := prEdge.Node

		reviewStatus := pr.ReviewDecision

		updatedAt, err := time.Parse(time.RFC3339, pr.UpdatedAt)
		if err != nil {
			// not really a fatal error, just log it
			logger.Warn("could not parse time", slog.String("updatedAt", pr.UpdatedAt), slog.String("pr_url", pr.Url))
			updatedAt = time.Time{}
		}

		lastPrCommenter := ""
		for _, c := range pr.Comments.Edges {
			lastPrCommenter = c.Node.Author.Login
		}

		unrespondedThreads := actionableThreads(pr, username)

		reviewUsers := make([]string, 0)
		for _, u := range pr.ReviewRequests.Nodes {
			reviewUsers = append(reviewUsers, u.RequestedReviewer.Login)
		}

		for _, a := range pr.Reviews.Edges {
			// for some reason, the "Reviews" graph can contain a separate
			// approval that is _not_ registered as the ReviewDecision,
			// something that went unnoticed for ~5 months of using this API.
			if a.Node.State == "APPROVED" {
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
			UnrespondedThreads:       unrespondedThreads, // TODO "threadsNeedingOurAction"
			Additions:                pr.Additions,
			Deletions:                pr.Deletions,
			ReviewRequestedFromUsers: reviewUsers,
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

func actionableThreads(pr prSearchResultGraphQl, myUsername string) int {
	unrespondedThreads := 0
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
		iReactedToLastComment := userReactedToComment(lastComment.Reactions, myUsername)

		if ownPr && lastCommenter != myUsername && !iReactedToLastComment {
			// someone else commented last, and this is our pr, and we haven't
			// acknowledged it yet with a reaction (emoji)
			unrespondedThreads++
			continue
		}

		if !ownPr && lastCommenter == myUsername {
			// we have the currently last word, the owner should reply or resolve the thread
			continue
		}

		threadStarter := t.Node.Comments.Nodes[0].Author.Login
		if threadStarter == myUsername && lastCommenter != myUsername && !iReactedToLastComment {
			// we started the thread, and it's still open (and someone else has
			// the last word), and we haven't acknowledged it yet with a
			// reaction (emoji)
			unrespondedThreads++
			continue
		}

		// not recorded so far: someone else started the thread, we
		// commented in the middle and someone else has the last word
	}

	return unrespondedThreads
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
