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
	"sort"
	"strconv"
	"strings"
	"time"

	"log/slog"

	"github.com/chelmertz/elly/internal/types"
)

var ErrClient = errors.New("github returned client error")
var ErrGithubServer = errors.New("github returned server error")
var ErrInvalidToken = errors.New("github token is invalid")

const DefaultAPIURL = "https://api.github.com"

type ErrRateLimited struct {
	UnblockedAt time.Time
}

func (e *ErrRateLimited) Error() string {
	return fmt.Sprintf("%v: rate limited, next allowed after %s", ErrClient, e.UnblockedAt)
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

// prCommitNodeGraphQl is the head commit of a PR. Named rather than anonymous
// so a test can build one without restating the whole shape - adding
// StatusCheckRollup to an inline literal broke every caller at once.
type prCommitNodeGraphQl struct {
	Commit struct {
		Author struct {
			Date  string
			Email string
			Name  string
		}
		// StatusCheckRollup is github's own aggregate over every check on the
		// head commit. Only the scalar state is taken here: the per-check
		// contexts are a connection, and its node cost would multiply by the
		// 100 PRs the search returns. The failing names come from
		// fetchFailingChecks, which runs only for a PR this field says is red.
		StatusCheckRollup *struct {
			State string
		}
	}
}

type prSearchResultGraphQl struct {
	Id             string // GraphQL node id, for follow-up queries on this PR
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
				Url string
			}
		}
	}
	Commits struct {
		Nodes []prCommitNodeGraphQl
	}
	ReviewThreads prReviewThreadConnectionGraphQl
	Reviews       struct {
		Edges []struct {
			Node struct {
				Author struct {
					Login string
				}
				Url         string
				State       string
				SubmittedAt string
			}
		}
	}
}

// prReviewThreadConnectionGraphQl is one page of a PR's review threads; the
// search query returns the first page, queryReviewThreadsPage the rest.
type prReviewThreadConnectionGraphQl struct {
	TotalCount int
	PageInfo   struct {
		HasNextPage bool
		EndCursor   string
	}
	Edges []struct {
		Node prReviewThreadGraphQl
	}
}

type prReviewThreadGraphQl struct {
	IsResolved  bool
	IsOutdated  bool
	IsCollapsed bool
	// FirstComment and LastComment are the aliases used in
	// querySearchPrsInvolvingUser, each holding at most one comment
	FirstComment struct {
		Nodes []prReviewThreadCommentGraphQl
	}
	LastComment struct {
		Nodes []prReviewThreadCommentGraphQl
	}
}

type prReviewThreadCommentGraphQl struct {
	Author struct {
		Login string
	}
	Url       string
	CreatedAt string // only fetched for the last comment
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

func ghExpiration(expiration string, logger *slog.Logger) (time.Time, bool) {
	// I used to get offset but recently got an error due to a named TZ instead
	// - let's try both (and more in the future, when they change their minds
	// again)
	for _, layout := range []string{
		"2006-01-02 15:04:05 -0700",
		"2006-01-02 15:04:05 MST",
	} {
		t, err := time.Parse(layout, expiration)
		if err == nil {
			if t.Before(time.Now().Add(10 * 24 * time.Hour)) {
				// less than 10 days left on token, warn!
				logger.Warn("github token expires soon", slog.Time("expires", t), slog.Int("days_left", int(time.Until(t).Hours()/24)))
			}
			return t, true
		}
	}

	logger.Error("could not parse github token expiration", slog.String("expiration", expiration))
	return time.Time{}, false
}

// serverErrorRetries is how many extra attempts a 5xx gets before the caller
// is told. Github answers this query in ~7s and intermittently gives up on it
// with a 502 (nginx) or a 504 whose body literally says "Please try
// resubmitting your request"; 6 such failures were logged in one day. Without
// a retry each one costs a full backoff interval - 7m30s against a 5m poll,
// so one blip more than doubles how stale the data gets.
//
// Shrinking the query does not help and was measured rather than assumed:
// search(first:100) takes 6.9s and first:50 takes 6.7s, so the cost is
// github's search backend and not the node expansion.
const serverErrorRetries = 2

// serverErrorBackoff is the pause before each retry. Short on purpose: this
// sits inside a poll that is already late, and the failure it absorbs is one
// github recovers from in seconds.
var serverErrorBackoff = []time.Duration{1 * time.Second, 3 * time.Second}

// graphqlRequest posts one query, retrying a 5xx a couple of times before
// giving up. Only 5xx is retried: a 4xx, a rate limit and a parse failure are
// all answers rather than hiccups, and repeating them helps nobody.
func graphqlRequest(baseURL, query, token string, logger *slog.Logger) ([]byte, error) {
	var body []byte
	var err error
	for attempt := 0; ; attempt++ {
		body, err = graphqlRequestOnce(baseURL, query, token, logger)
		if err == nil || !errors.Is(err, ErrGithubServer) || attempt >= serverErrorRetries {
			return body, err
		}
		pause := serverErrorBackoff[min(attempt, len(serverErrorBackoff)-1)]
		logger.Warn("github server error, retrying",
			slog.Int("attempt", attempt+1),
			slog.Duration("in", pause),
			slog.Any("err", err))
		githubRequestsTotal.WithLabelValues("server_error_retried").Inc()
		time.Sleep(pause)
	}
}

func graphqlRequestOnce(baseURL, query, token string, logger *slog.Logger) ([]byte, error) {
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

	request, err := http.NewRequestWithContext(ctx, "POST", baseURL+"/graphql", bytes.NewReader(jsonBytes))
	if err != nil {
		return nil, fmt.Errorf("could not construct github request: %w", err)
	}
	request.Header.Add("Authorization", "bearer "+token)

	logger.Debug("querying github api")
	response, err := httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("could not request github: %w", err)
	}
	defer response.Body.Close() //nolint:errcheck // error on close is not actionable

	if expiration := response.Header.Get("Github-Authentication-Token-Expiration"); expiration != "" {
		_, _ = ghExpiration(expiration, logger)
	}

	respBody, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, fmt.Errorf("could not read github username response: %w", err)
	}

	// 5xx bodies are not reliably json (502s come as html), so decide on
	// the status code before trying to parse anything
	if response.StatusCode >= 500 {
		logger.Warn("response", slog.Int("response_code", response.StatusCode), slog.String("body", string(respBody)))
		return nil, fmt.Errorf("%w: github response code %d", ErrGithubServer, response.StatusCode)
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
		return nil, fmt.Errorf("%w: json unmarshal error", ErrClient)
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

				// Example of response found in logs, but note that there are
				// more properties than `error` that might look like a success
				// (HTTP's status codes are overrated?):
				//
				// {"errors":[{"type":"RATE_LIMITED","message":"API rate limit exceeded for user ID 123456."}]}
				var earliestRetry time.Time
				xRateLimitReset := response.Header.Get("x-ratelimit-reset")
				if xRateLimitReset != "" {
					xRateLimitResetInt, err := strconv.ParseInt(xRateLimitReset, 10, 64)
					if err == nil {
						earliestRetry = time.Unix(xRateLimitResetInt, 0)
					}
				}
				retryAfter := response.Header.Get("retry-after")
				if retryAfter != "" {
					retryAfterInt, err := strconv.ParseInt(retryAfter, 10, 64)
					if err == nil {
						retryAfterDate := time.Now().Add(time.Duration(retryAfterInt) * time.Second)
						if retryAfterDate.After(earliestRetry) {
							earliestRetry = retryAfterDate
						}
					}
				}
				if earliestRetry.IsZero() {
					logger.Warn("github rate limited, no retry time found", slog.Any("response_body_graphql_errors", errorResponse.Errors), slog.Any("response_headers", response.Header))
					return nil, fmt.Errorf("%w: github rate limited, no retry time found", ErrClient)
				} else {
					logger.Error("github rate limited", slog.Any("response_body_graphql_errors", errorResponse.Errors), slog.Time("earliest_retry", earliestRetry), slog.String("header_x-ratelimit-reset", xRateLimitReset), slog.String("header_retry-after", retryAfter))
					return nil, &ErrRateLimited{UnblockedAt: earliestRetry}
				}
			}
		}
	}

	logger.Debug("github response", slog.String("body", string(respBody)), slog.Int("status", response.StatusCode))

	if response.StatusCode >= 400 {
		logger.Warn("response", slog.Int("response_code", response.StatusCode), slog.String("body", string(respBody)))
		return nil, fmt.Errorf("%w: github response code %d", ErrClient, response.StatusCode)
	}
	return respBody, nil
}

// ValidatePAT validates a PAT by checking authentication and required scopes.
// Returns username, expiration time, and error if invalid.
func ValidatePAT(baseURL, token string, logger *slog.Logger) (username string, expiresAt time.Time, err error) {
	username, expiresAt, err = UsernameFromPat(baseURL, token, logger)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("token authentication failed: %w", err)
	}

	// Validate scopes by attempting a PR query
	_, _, err = QueryGithub(baseURL, token, username, logger)
	if err != nil {
		// Client errors (except rate limiting) indicate the token lacks
		// required permissions — treat as invalid token.
		var rl *ErrRateLimited
		if errors.Is(err, ErrClient) && !errors.As(err, &rl) {
			return "", time.Time{}, fmt.Errorf("%w: token lacks required permissions: %w", ErrInvalidToken, err)
		}
		// Transient errors (rate limit, server error, network) — propagate as-is.
		return "", time.Time{}, fmt.Errorf("token validation failed: %w", err)
	}

	return username, expiresAt, nil
}

// UsernameFromPat returns the username and expiration date for the given PAT.
// The expiration time is zero if the token doesn't expire.
func UsernameFromPat(baseURL, token string, logger *slog.Logger) (username string, expiresAt time.Time, err error) {
	query := `query { viewer { login } }`
	payload := struct {
		Query string `json:"query"`
	}{
		Query: query,
	}
	jsonBytes, err := json.Marshal(payload)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("could not marshal graphql json: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
	defer cancel()

	httpClient := &http.Client{}
	request, err := http.NewRequestWithContext(ctx, "POST", baseURL+"/graphql", bytes.NewReader(jsonBytes))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("could not construct github request: %w", err)
	}
	request.Header.Add("Authorization", "bearer "+token)

	response, err := httpClient.Do(request)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("could not request github: %w", err)
	}
	defer response.Body.Close() //nolint:errcheck // error on close is not actionable

	if expiration := response.Header.Get("Github-Authentication-Token-Expiration"); expiration != "" {
		if parsed, ok := ghExpiration(expiration, logger); ok {
			expiresAt = parsed
		}
	}

	if response.StatusCode == 401 || response.StatusCode == 403 {
		return "", time.Time{}, fmt.Errorf("%w: github response code %d", ErrInvalidToken, response.StatusCode)
	}
	if response.StatusCode >= 400 {
		if response.StatusCode < 500 {
			return "", time.Time{}, fmt.Errorf("%w: github response code %d", ErrClient, response.StatusCode)
		}
		return "", time.Time{}, fmt.Errorf("%w: github response code %d", ErrGithubServer, response.StatusCode)
	}

	respBody, err := io.ReadAll(response.Body)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("could not read github username response: %w", err)
	}

	var typedResponse struct {
		Data struct {
			Viewer struct {
				Login string
			}
		}
		Errors []struct {
			Type    string
			Message string
		}
	}
	err = json.Unmarshal(respBody, &typedResponse)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("could not unmarshal github username response: %w", err)
	}

	if len(typedResponse.Errors) > 0 {
		return "", time.Time{}, fmt.Errorf("%w: github returned errors: %v", ErrClient, typedResponse.Errors)
	}

	return typedResponse.Data.Viewer.Login, expiresAt, nil
}

var ignoredLastPrCommenters = []string{"github-actions", "vercel"}

func QueryGithub(baseURL, token string, username string, logger *slog.Logger) ([]types.ViewPr, []types.Degradation, error) {
	prs, degradations, err := queryGithub(baseURL, token, username, logger)
	if err != nil {
		var rl *ErrRateLimited
		if errors.As(err, &rl) {
			githubRequestsTotal.WithLabelValues("rate_limited").Inc()
			rateLimitEventsTotal.Inc()
		} else if errors.Is(err, ErrGithubServer) {
			githubRequestsTotal.WithLabelValues("server_error").Inc()
		} else {
			githubRequestsTotal.WithLabelValues("client_error").Inc()
		}
		return nil, nil, err
	}
	githubRequestsTotal.WithLabelValues("success").Inc()
	return prs, degradations, nil
}

func queryGithub(baseURL, token string, username string, logger *slog.Logger) ([]types.ViewPr, []types.Degradation, error) {
	// Keyed by Kind so one refusal repeated across every red PR is reported once.
	degradations := map[string]types.Degradation{}
	respBody, err := graphqlRequest(baseURL, querySearchPrsInvolvingUser(username), token, logger)
	if err != nil {
		return nil, nil, fmt.Errorf("could not query github for PRs: %w", err)
	}

	// Using json.RawMessage for the response, so that we can store the raw
	// JSON (not the parsed response) of each PR, for debugging reasons.
	// Debugging > efficiency, in this case.
	var rawResponse querySearchPrsInvolvingMeGraphQl
	err = json.Unmarshal(respBody, &rawResponse)
	if err != nil {
		return nil, nil, fmt.Errorf("could not unmarshal github response: %w", err)
	}

	viewPrs := make([]types.ViewPr, 0)

	for _, prEdge := range rawResponse.Data.Search.Edges {
		var pr prSearchResultGraphQl
		err = json.Unmarshal(prEdge.Node, &pr)
		if err != nil {
			return nil, nil, fmt.Errorf("could not re-marshal github PR, to store raw json for debugging (url=%s): %w", pr.Url, err)
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

		if err := fetchRemainingReviewThreads(baseURL, token, &pr, logger); err != nil {
			// the poll goes on with what the search returned; the count for
			// this PR is a lower bound until the next poll succeeds
			logger.Warn("could not fetch all review threads", slog.String("pr_url", pr.Url), slog.Any("err", err))
		}
		threadsActionable, threadsWaiting := actionableThreads(pr, username)

		reviewUsers := make([]string, 0)
		for _, u := range pr.ReviewRequests.Nodes {
			reviewUsers = append(reviewUsers, u.RequestedReviewer.Login)
		}
		rereview := rereviewFrom(pr, username, threadsActionable)

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

		// CI state on the head commit. The rollup scalar rides along with the
		// search query; the failing names cost a follow-up, so that is only
		// paid when the rollup already says the PR is red.
		checksState := ""
		if len(pr.Commits.Nodes) > 0 && pr.Commits.Nodes[0].Commit.StatusCheckRollup != nil {
			checksState = strings.ToUpper(pr.Commits.Nodes[0].Commit.StatusCheckRollup.State)
		}
		var checksFailing []string
		checksComplete := true
		if checksState == "FAILURE" || checksState == "ERROR" {
			var degraded *types.Degradation
			checksFailing, checksComplete, degraded = fetchFailingChecks(baseURL, token, pr.Id, pr.Url, logger)
			if degraded != nil {
				degradations[degraded.Kind] = *degraded
			}
			// The rollup says red, so at least one check failed. Coming back
			// with no names means they could not be read, not that there are
			// none - never let that render as "red, 0 failing checks".
			if len(checksFailing) == 0 {
				checksComplete = false
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
			RereviewFrom:             rereview,
			ChecksState:              checksState,
			ChecksFailing:            checksFailing,
			ChecksComplete:           checksComplete,
			RawJsonResponse:          prEdge.Node,
		}
		logger.Debug("fetched a pr", slog.Any("pr", viewPr))
		viewPrs = append(viewPrs, viewPr)
	}

	out := make([]types.Degradation, 0, len(degradations))
	for _, d := range degradations {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Kind < out[j].Kind })
	return viewPrs, out, nil
}

// rereviewFrom names the reviewers of the user's own PR who should be asked
// to look again: they left a non-approving review, the user has since
// pushed or replied, every thread is answered, and no re-review has been
// requested. Github shows such a PR as simply "review required", and the
// reviewer has no signal that the ball came back to them. Drafts are left
// out: a draft is not asking for review.
func rereviewFrom(pr prSearchResultGraphQl, myUsername string, threadsActionable int) []string {
	if pr.Author.Login != myUsername || pr.IsDraft || threadsActionable > 0 {
		return []string{}
	}
	myLast := time.Time{}
	bump := func(s string) {
		if t, err := time.Parse(time.RFC3339, s); err == nil && t.After(myLast) {
			myLast = t
		}
	}
	for _, c := range pr.Commits.Nodes {
		bump(c.Commit.Author.Date) // commits on my PR are taken as mine
	}
	for _, c := range pr.Comments.Edges {
		if c.Node.Author.Login == myUsername {
			bump(c.Node.UpdatedAt)
		}
	}
	for _, t := range pr.ReviewThreads.Edges {
		for _, c := range t.Node.LastComment.Nodes {
			if c.Author.Login == myUsername {
				bump(c.CreatedAt)
			}
		}
	}
	if myLast.IsZero() {
		return []string{}
	}
	requested := make(map[string]bool)
	for _, u := range pr.ReviewRequests.Nodes {
		requested[u.RequestedReviewer.Login] = true
	}
	// the latest review per reviewer decides; reviews come oldest first
	latest := make(map[string]struct {
		state string
		at    time.Time
	})
	var order []string
	for _, r := range pr.Reviews.Edges {
		login := r.Node.Author.Login
		if login == myUsername || login == "" || strings.HasSuffix(login, "[bot]") || slices.Contains(ignoredLastPrCommenters, login) {
			continue
		}
		at, err := time.Parse(time.RFC3339, r.Node.SubmittedAt)
		if err != nil {
			continue
		}
		if _, seen := latest[login]; !seen {
			order = append(order, login)
		}
		latest[login] = struct {
			state string
			at    time.Time
		}{r.Node.State, at}
	}
	out := make([]string, 0)
	for _, login := range order {
		l := latest[login]
		if (l.state == "COMMENTED" || l.state == "CHANGES_REQUESTED") && l.at.Before(myLast) && !requested[login] {
			out = append(out, login)
		}
	}
	return out
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

		if len(t.Node.FirstComment.Nodes) == 0 || len(t.Node.LastComment.Nodes) == 0 {
			// the types say this is possible, I haven't seen it in the wild though
			continue
		}

		lastComment := t.Node.LastComment.Nodes[0]
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

		threadStarter := t.Node.FirstComment.Nodes[0].Author.Login
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

// firstReviewThreadsPage is how many review threads the search query carries
// per PR. Github prices a query by the product of nested first/last
// arguments, so this is multiplied by 100 PRs, 2 comments and 7 reactions;
// keep it small and let the per-PR follow-up pay for big PRs only.
const firstReviewThreadsPage = "15"

// reviewThreadPageSize is the page size of the per-PR follow-up query, whose
// cost is not multiplied by the PR count.
const reviewThreadPageSize = "100"

// maxReviewThreadPages bounds the follow-up loop: 10 pages of 100 threads is
// far beyond any PR seen; past it the count is logged as truncated.
const maxReviewThreadPages = 10

// checkContextPageSize is the page size of the per-PR failing-checks query.
// Like the review-thread follow-up its cost is not multiplied by the PR count,
// because it runs only for a PR whose rollup state is already FAILURE.
const checkContextPageSize = "100"

// maxCheckContextPages bounds that loop. One matchi-backend PR carries 138
// contexts; ten pages is far beyond anything seen, and past it the names are
// reported as a lower bound while the state stays authoritative.
const maxCheckContextPages = 10

// failedConclusions are the check conclusions that mean "this is red". CANCELLED
// is included deliberately: a cancelled required check blocks a merge exactly
// like a failing one, and reading it as "not failing" is how a red PR gets
// reported as ready for review.
var failedConclusions = map[string]bool{
	"FAILURE": true, "ERROR": true, "TIMED_OUT": true,
	"CANCELLED": true, "ACTION_REQUIRED": true, "STARTUP_FAILURE": true,
}

// queryFailingChecks names the failing checks on one PR's head commit, by node
// id the way queryReviewThreadsPage does. It is only ever called for a PR whose
// statusCheckRollup state is already FAILURE.
func queryFailingChecks(prNodeId, after string) string {
	cursor := "null"
	if after != "" {
		cursor = `"` + after + `"`
	}
	return fmt.Sprintf(`query {
  node(id: %q) {
    ... on PullRequest {
      commits(last: 1) {
        nodes {
          commit {
            statusCheckRollup {
              contexts(first: `+checkContextPageSize+`, after: %s) {
                totalCount
                pageInfo { hasNextPage endCursor }
                nodes {
                  __typename
                  ... on CheckRun { name conclusion status }
                  ... on StatusContext { context state }
                }
              }
            }
          }
        }
      }
    }
  }
}`, prNodeId, cursor)
}

// checkContext is one entry of a statusCheckRollup, flattened across the two
// shapes github uses: CheckRun (actions) carries name/conclusion, StatusContext
// (external reporters like buildkite) carries context/state.
type checkContext struct {
	TypeName   string `json:"__typename"`
	Name       string
	Conclusion string
	Status     string
	Context    string
	State      string
}

func (c checkContext) label() string {
	if c.Name != "" {
		return c.Name
	}
	if c.Context != "" {
		return c.Context
	}
	return "(unnamed check)"
}

func (c checkContext) failed() bool {
	verdict := c.Conclusion
	if verdict == "" {
		verdict = c.State
	}
	return failedConclusions[strings.ToUpper(verdict)]
}

// fetchFailingChecks returns the names of the failing checks on a PR, and
// whether the list is complete. A PR with more contexts than maxCheckContextPages
// covers returns what it read plus false, so a caller can say "at least N"
// rather than silently under-reporting - the exact failure this whole column
// exists to prevent.
func fetchFailingChecks(baseURL, token, prNodeId, prUrl string, logger *slog.Logger) ([]string, bool, *types.Degradation) {
	var failing []string
	cursor := ""
	for pages := 0; pages < maxCheckContextPages; pages++ {
		body, err := graphqlRequest(baseURL, queryFailingChecks(prNodeId, cursor), token, logger)
		if err != nil {
			logger.Warn("could not fetch failing checks", slog.String("pr_url", prUrl), slog.Any("err", err))
			return failing, false, nil
		}
		var page struct {
			Data struct {
				Node struct {
					Commits struct {
						Nodes []struct {
							Commit struct {
								StatusCheckRollup *struct {
									Contexts struct {
										TotalCount int
										PageInfo   struct {
											HasNextPage bool
											EndCursor   string
										}
										Nodes []checkContext
									}
								}
							}
						}
					}
				}
			}
		}
		if err := json.Unmarshal(body, &page); err != nil {
			logger.Warn("could not parse failing checks", slog.String("pr_url", prUrl), slog.Any("err", err))
			return failing, false, nil
		}
		// GraphQL reports per-field failures in a top-level "errors" array while
		// still answering 200 with nulls in place of the nodes it refused. A
		// fine-grained PAT without "Checks: read" does exactly that: the rollup
		// state resolves, every context comes back null, and the response is
		// otherwise indistinguishable from a PR with nothing failing. Reporting
		// that as a complete, empty list is a lie about a red PR, so it is
		// reported as incomplete instead.
		var envelope struct {
			Errors []struct {
				Type    string
				Message string
			}
		}
		if err := json.Unmarshal(body, &envelope); err == nil && len(envelope.Errors) > 0 {
			logger.Warn("github refused some check contexts",
				slog.String("pr_url", prUrl),
				slog.String("type", envelope.Errors[0].Type),
				slog.String("message", envelope.Errors[0].Message),
				slog.String("hint", "a fine-grained PAT needs the Checks and Commit statuses read permissions"))
			degraded := &types.Degradation{
				Kind:    "checks_unreadable",
				Message: "Github refuses to name the failing checks: " + envelope.Errors[0].Message,
				Seen:    time.Now(),
			}
			if envelope.Errors[0].Type == "FORBIDDEN" {
				degraded.Remedy = "Grant this token read access to \"Checks\" (Github Actions jobs) " +
					"and \"Commit statuses\" (buildkite and other external reporters). " +
					"Red or green stays correct without them; only the names of the failing checks are missing."
			}
			return failing, false, degraded
		}
		if len(page.Data.Node.Commits.Nodes) == 0 || page.Data.Node.Commits.Nodes[0].Commit.StatusCheckRollup == nil {
			return failing, true, nil
		}
		contexts := page.Data.Node.Commits.Nodes[0].Commit.StatusCheckRollup.Contexts
		for _, c := range contexts.Nodes {
			if c.failed() {
				failing = append(failing, c.label())
			}
		}
		if !contexts.PageInfo.HasNextPage {
			return failing, true, nil
		}
		cursor = contexts.PageInfo.EndCursor
	}
	logger.Warn("failing checks truncated", slog.String("pr_url", prUrl), slog.Int("found", len(failing)))
	return failing, false, nil
}

// reviewThreadFields are the review thread fields both queries fetch. Only
// the first comment's author and the last comment's author and reactions are
// inspected (aliases are matched by field name in prReviewThreadGraphQl).
const reviewThreadFields = `
                isResolved
                isOutdated
                isCollapsed
                firstComment: comments(first: 1) {
                  nodes {
                    author {
                      login
                    }
                  }
                }
                lastComment: comments(last: 1) {
                  nodes {
                    author {
                      login
                    }
                    url
                    createdAt
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
                }`

// queryReviewThreadsPage fetches one more page of a PR's review threads.
func queryReviewThreadsPage(prID, after string) string {
	return fmt.Sprintf(`query {
  node(id: %s) {
    ... on PullRequest {
      reviewThreads(first: `+reviewThreadPageSize+`, after: %s) {
        totalCount
        pageInfo {
          hasNextPage
          endCursor
        }
        edges {
          node {`+reviewThreadFields+`
          }
        }
      }
    }
  }
}`, strconv.Quote(prID), strconv.Quote(after))
}

// fetchRemainingReviewThreads appends the pages after the first to
// pr.ReviewThreads.Edges, so actionableThreads sees every thread. Before
// this, a PR with more than one page of threads (80 on one PR seen
// 2026-09-08) had its newest threads, which come last, silently ignored,
// and reported zero actionable threads.
func fetchRemainingReviewThreads(baseURL, token string, pr *prSearchResultGraphQl, logger *slog.Logger) error {
	pages := 0
	for pr.ReviewThreads.PageInfo.HasNextPage {
		if pages == maxReviewThreadPages {
			logger.Warn("review threads truncated", slog.String("pr_url", pr.Url), slog.Int("fetched", len(pr.ReviewThreads.Edges)), slog.Int("total", pr.ReviewThreads.TotalCount))
			return nil
		}
		pages++
		body, err := graphqlRequest(baseURL, queryReviewThreadsPage(pr.Id, pr.ReviewThreads.PageInfo.EndCursor), token, logger)
		if err != nil {
			return fmt.Errorf("review threads page %d of %s: %w", pages+1, pr.Url, err)
		}
		var page struct {
			Data struct {
				Node struct {
					ReviewThreads prReviewThreadConnectionGraphQl
				}
			}
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return fmt.Errorf("review threads page %d of %s: %w", pages+1, pr.Url, err)
		}
		pr.ReviewThreads.Edges = append(pr.ReviewThreads.Edges, page.Data.Node.ReviewThreads.Edges...)
		pr.ReviewThreads.PageInfo = page.Data.Node.ReviewThreads.PageInfo
	}
	return nil
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
          id
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
                # scalar only - see StatusCheckRollup in the struct above
                statusCheckRollup {
                  state
                }
              }
            }
          }
          # threads can't be filtered on isResolved server-side, so every
          # thread is fetched: this first page here, the rest per PR through
          # queryReviewThreadsPage (see fetchRemainingReviewThreads). The
          # page is kept small because its cost multiplies with the 100 PRs
          # above; a follow-up query only costs what that one PR has.
          reviewThreads(first: ` + firstReviewThreadsPage + `) {
            totalCount
            pageInfo {
              hasNextPage
              endCursor
            }
            edges {
              node {` + reviewThreadFields + `
              }
            }
          }
          reviews(first: 20) {
            edges {
                node {
                    author {
                        login
                    }
                    url
                    state
                    submittedAt
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
