package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io"
	"math"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"

	"golang.org/x/exp/slog"
)

func check(err error) {
	if err != nil {
		panic(err)
	}
}

var timeoutMinutes = flag.Int("timeout", 10, "refresh PRs every N minutes")
var url = flag.String("url", "localhost:9876", "URL for web GUI")
var logger = slog.New(slog.NewTextHandler(os.Stdout))

func main() {
	flag.Parse()

	token := os.Getenv("GITHUB_PAT")
	if token == "" {
		logger.Error("missing GITHUB_PAT env var", nil)
		os.Exit(1)
	}

	username := os.Getenv("GITHUB_USER")
	if token == "" {
		logger.Warn("missing GITHUB_USER env var, will not assign points properly", nil)
	}

	StartRefreshLoop(token, username)
	ServeWeb(*url, username, token)
}

func StartRefreshLoop(token, username string) {
	refreshTimer := time.NewTicker(time.Duration(*timeoutMinutes) * time.Minute)
	githubFailure := make(chan struct{})
	manualRefresh := make(chan string)
	retriesLeft := 5

	storage := readStorage()

	go func() {
		for {
			select {
			case reason := <-manualRefresh:
				logger.Info("manual refresh requested", slog.String("reason", reason))
				_, err := possiblyRefreshPrs(token, username, storage.LastFetched, githubFailure)
				if err != nil {
					logger.Error("error refreshing PRs", err, slog.Int("retries_left", retriesLeft))
				}
			case <-refreshTimer.C:
				_, err := possiblyRefreshPrs(token, username, storage.LastFetched, githubFailure)
				if err != nil {
					logger.Error("error refreshing PRs", err, slog.Int("retries_left", retriesLeft))
				}
			case <-githubFailure:
				retriesLeft--
				if retriesLeft <= 0 {
					refreshTimer.Stop()
					logger.Error("too many failed github requests, giving up", nil)
					return
				}
			}
		}
	}()
	manualRefresh <- "upstart refresh"
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

type storage struct {
	Prs         []ViewPr
	LastFetched time.Time
}

var storageMutex sync.Mutex

func readStorage() storage {
	storageMutex.Lock()
	defer storageMutex.Unlock()
	oldContents, err := os.ReadFile(filename)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			oldContents = []byte("{}")
		} else {
			check(err)
		}
	}

	storage := storage{}
	err = json.Unmarshal(oldContents, &storage)
	check(err)

	return storage
}

var filename = "prs.json"

func storeRepoPrs(orderedPrs []ViewPr) error {
	storageMutex.Lock()
	defer storageMutex.Unlock()
	storage := storage{}
	storage.Prs = make([]ViewPr, len(orderedPrs))
	copy(storage.Prs, orderedPrs)
	storage.LastFetched = time.Now()

	logger.Info("storing prs", slog.Int("prs", len(storage.Prs)))

	newContents, err := json.Marshal(storage)
	if err != nil {
		return fmt.Errorf("could not marshal json: %w", err)
	}

	if err := os.WriteFile(filename, newContents, 0660); err != nil {
		return fmt.Errorf("could not write file: %w", err)
	}

	return nil
}

type IndexHtmlData struct {
	Prs            []ViewPr
	PointsPerPrUrl map[string]*Points
	CurrentUser    string
	RefreshUrl     string
	LastRefreshed  string
}

type Points struct {
	Total   int
	Reasons []string
}

func (p *Points) Add(points int, reason string) {
	p.Total += points
	reasonWithPrefix := fmt.Sprintf("+%d: %s", points, reason)
	p.Reasons = append(p.Reasons, reasonWithPrefix)
}

func (p *Points) Remove(points int, reason string) {
	p.Total -= points
	reasonWithPrefix := fmt.Sprintf("-%d: %s", points, reason)
	p.Reasons = append(p.Reasons, reasonWithPrefix)
}

// ViewPr must contain everything needed to order/compare them against other PRs,
// since ViewPr is also what we store.
type ViewPr struct {
	ReviewStatus                         string
	Url                                  string
	Title                                string
	Author                               string
	RepoName                             string
	RepoUrl                              string
	IsDraft                              bool
	LastUpdated                          time.Time
	LastPrCommenter                      string
	UnresolvedReviewThreadLastCommenters []string
	Additions                            int
	Deletions                            int
}

func possiblyRefreshPrs(token, username string, lastFetched time.Time, githubFailure chan struct{}) (bool, error) {
	if time.Since(lastFetched) < time.Duration(*timeoutMinutes)*time.Minute {
		return false, nil
	}
	prs, err := queryGithub(token, username)
	if err != nil {
		githubFailure <- struct{}{}
		return false, fmt.Errorf("could not query github: %w", err)
	}

	if err := storeRepoPrs(prs); err != nil {
		return false, fmt.Errorf("could not store prs: %w", err)
	}
	return true, nil
}

func ServeWeb(url string, username string, token string) {
	temp, err := template.
		New("index.html").
		ParseGlob("*.html")
	check(err)

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		var storage storage = readStorage()
		prs := storage.Prs

		pointsPerPrUrl := make(map[string]*Points)
		for _, pr := range prs {
			pointsPerPrUrl[pr.Url] = standardPrPoints(pr, username)
		}

		sort.Slice(prs, func(i, j int) bool {
			pri := pointsPerPrUrl[prs[i].Url].Total
			prj := pointsPerPrUrl[prs[j].Url].Total
			if pri == prj {
				lastUpdated := prs[i].LastUpdated.Before(prs[j].LastUpdated)
				return lastUpdated
			}
			return pri > prj
		})
		lastRefreshed := fmt.Sprintf("%d min ago (%d:%d)",
			int(math.RoundToEven(time.Since(storage.LastFetched).Minutes())),
			storage.LastFetched.Local().Hour(),
			storage.LastFetched.Local().Minute())
		data := IndexHtmlData{
			Prs:            prs,
			PointsPerPrUrl: pointsPerPrUrl,
			CurrentUser:    username,
			LastRefreshed:  lastRefreshed,
		}
		err := temp.Execute(w, data)
		check(err)
	})

	logger.Info("starting web server at", slog.String("url", "http://"+url))
	err = http.ListenAndServe(url, nil)
	check(err)
}

// unrespondedCommentThreads() searches through PR review comments and returns
// amount of comments that the given github user has not responded to.
func unrespondedCommentThreads(pr ViewPr, myGithubUsername string) int {
	comments := 0
	if myGithubUsername == "" {
		return comments
	}

	for _, c := range pr.UnresolvedReviewThreadLastCommenters {
		if c != myGithubUsername {
			comments++
		}
	}

	return comments
}

// standardPrPoints() awards points to PRs based on a set of rules.
// These rules should be revisited often, and the points should be tweaked.
func standardPrPoints(pr ViewPr, username string) *Points {
	points := &Points{}
	points.Reasons = make([]string, 0)

	unrespondedThreads := unrespondedCommentThreads(pr, username)

	if pr.Author == username {
		// our pr
		if pr.ReviewStatus == "APPROVED" {
			points.Add(100, "Own PR is approved, should be a simple merge")
		}

		if pr.LastPrCommenter != username {
			// someone might have asked us something
			points.Add(10, fmt.Sprintf("Someone else commented last (%s)", pr.LastPrCommenter))
		}

		if unrespondedThreads > 0 {
			points.Add(80, fmt.Sprintf("Someone asked us something (%d comments)", unrespondedThreads))
			// we already need to go over this, don't scale the points
			// by amount of threads though, it might go overboard
		}

		if pr.IsDraft {
			points.Remove(10, "PR is my draft")
		}
	} else {
		// someone else's pr, or our but the username is not set
		if pr.ReviewStatus == "APPROVED" {
			points.Remove(100, "PR is someone else's and is approved")
		}

		if pr.IsDraft {
			points.Remove(100, "PR is someone else's draft")
		}

		// reward short prs
		diff := pr.Additions + pr.Deletions
		switch {
		case diff < 50:
			points.Add(50, fmt.Sprintf("PR is small, %d loc changed is <50", diff))
		case diff < 150:
			points.Add(30, fmt.Sprintf("PR is smallish, %d loc changed is <150", diff))
		case diff > 300:
			points.Remove(30, fmt.Sprintf("PR is bigish, %d loc changed is >300", diff))
		}

		// TODO find our own comment threads here, and see if they are
		// responded to
	}

	sort.Slice(points.Reasons, func(i, j int) bool {
		// render all + points first, then - points
		return points.Reasons[i] < points.Reasons[j]
	})

	return points
}

type querySearchPrsInvolvingMeGraphQl struct {
	Data struct {
		Search struct {
			Edges []struct {
				Node struct {
					Url            string
					Title          string
					IsDraft        bool
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
	respBody, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, fmt.Errorf("could not read github response: %w", err)
	}

	typedResponse := querySearchPrsInvolvingMeGraphQl{}
	err = json.Unmarshal(respBody, &typedResponse)
	if err != nil {
		return nil, fmt.Errorf("could not unmarshal github response: %w", err)
	}

	logger.Debug("response", slog.Any("serialized", typedResponse), slog.String("body", string(respBody)))

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

		unresolvedReviewThreadLastCommenters := make([]string, 0)
		for _, t := range pr.ReviewThreads.Edges {
			if t.Node.IsCollapsed || t.Node.IsOutdated || t.Node.IsResolved {
				continue
			}

			lastCommenting := t.Node.Comments.Nodes[len(t.Node.Comments.Nodes)-1].Author.Login
			unresolvedReviewThreadLastCommenters = append(unresolvedReviewThreadLastCommenters, lastCommenting)
		}

		viewPr := ViewPr{
			ReviewStatus:                         pr.ReviewDecision,
			Url:                                  pr.Url,
			Title:                                pr.Title,
			Author:                               pr.Author.Login,
			RepoName:                             pr.Repository.Name,
			RepoUrl:                              pr.Repository.Url,
			IsDraft:                              pr.IsDraft,
			LastUpdated:                          updatedAt,
			LastPrCommenter:                      lastPrCommenter,
			UnresolvedReviewThreadLastCommenters: unresolvedReviewThreadLastCommenters,
			Additions:                            pr.Additions,
			Deletions:                            pr.Deletions,
		}
		logger.Debug("fetched a pr", slog.Any("pr", viewPr))
		viewPrs = append(viewPrs, viewPr)
	}

	return viewPrs, nil
}
