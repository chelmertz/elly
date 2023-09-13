package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"golang.org/x/exp/slog"
)

var errClient = errors.New("github returned client error")
var errGithubServer = errors.New("github returned server error")
var errCouldNotStorePrs = errors.New("could not store prs")

func possiblyRefreshPrs(token, username string, storage *storage) (bool, error) {
	// querying github once a minute should be fine,
	// especially as long as we do the passive, loopy thing more seldom
	if time.Since(storage.Prs().LastFetched) < time.Duration(59)*time.Second {
		return false, nil
	}
	prs, err := queryGithub(token, username)
	if err != nil {
		return false, fmt.Errorf("could not query github: %w", err)
	}

	if err := storage.StoreRepoPrs(prs); err != nil {
		return false, fmt.Errorf("%w: %w", errCouldNotStorePrs, err)
	}
	return true, nil
}

type querySearchPrsInvolvingMeGraphQl struct {
	Data struct {
		Search struct {
			Edges []struct {
				Node struct {
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
								Url  string
								Body string
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
							Node struct {
								IsResolved  bool
								IsOutdated  bool
								IsCollapsed bool
								Comments    struct {
									Nodes []struct {
										Author struct {
											Login string
										}
										Body string
										Url  string
									}
								}
							}
						}
					}
				}
			}
		}
	}
}

func queryGithub(token string, username string) ([]ViewPr, error) {
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
			// TODO notify the GUI
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
			return nil, errClient
		}
		return nil, errGithubServer
	}

	typedResponse := querySearchPrsInvolvingMeGraphQl{}
	err = json.Unmarshal(respBody, &typedResponse)
	if err != nil {
		return nil, fmt.Errorf("could not unmarshal github response: %w", err)
	}

	viewPrs := make([]ViewPr, 0)

	for _, prEdge := range typedResponse.Data.Search.Edges {
		pr := prEdge.Node

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

		ownPr := pr.Author.Login == username
		unrespondedThreads := 0
		for _, t := range pr.ReviewThreads.Edges {
			if t.Node.IsCollapsed || t.Node.IsOutdated || t.Node.IsResolved {
				continue
			}

			lastCommenter := t.Node.Comments.Nodes[len(t.Node.Comments.Nodes)-1].Author.Login
			if lastCommenter == username {
				// we have the (currently) last word
				continue
			}

			if ownPr {
				// someone else commented last, and this is our pr
				unrespondedThreads++
				continue
			}

			threadStarter := t.Node.Comments.Nodes[0].Author.Login
			if threadStarter == username {
				// we started the thread, and it's still open (and someone else
				// has the last word)
				unrespondedThreads++
				continue
			}

			// not recorded so far: someone else started the thread, we
			// commented in the middle and someone else has the last word
		}

		reviewUsers := make([]string, 0)
		for _, u := range pr.ReviewRequests.Nodes {
			reviewUsers = append(reviewUsers, u.RequestedReviewer.Login)
		}

		viewPr := ViewPr{
			ReviewStatus:             pr.ReviewDecision,
			Url:                      pr.Url,
			Title:                    pr.Title,
			Author:                   pr.Author.Login,
			RepoName:                 pr.Repository.Name,
			RepoOwner:                pr.Repository.Owner.Login,
			RepoUrl:                  pr.Repository.Url,
			IsDraft:                  pr.IsDraft,
			LastUpdated:              updatedAt,
			LastPrCommenter:          lastPrCommenter,
			UnrespondedThreads:       unrespondedThreads,
			Additions:                pr.Additions,
			Deletions:                pr.Deletions,
			ReviewRequestedFromUsers: reviewUsers,
		}
		logger.Debug("fetched a pr", slog.Any("pr", viewPr))
		viewPrs = append(viewPrs, viewPr)
	}

	return viewPrs, nil
}

func querySearchPrsInvolvingUser(username string) string {
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
          reviewThreads(first: 20) {
            edges {
              node {
                isResolved
                isOutdated
                isCollapsed
                comments(first: 100) {
                  nodes {
                    author {
                      login
                    }
                    body
                    url
                  }
                }
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
