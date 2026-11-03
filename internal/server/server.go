package server

import (
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/chelmertz/elly/internal/backoff"
	"github.com/chelmertz/elly/internal/github"
	"github.com/chelmertz/elly/internal/points"
	"github.com/chelmertz/elly/internal/storage"
	"github.com/chelmertz/elly/internal/types"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func check(err error) {
	if err != nil {
		panic(err)
	}
}

type IndexHtmlData struct {
	Prs                    []types.ViewPr
	PointsPerPrUrl         map[string]*points.Points
	CurrentUser            string
	RefreshUrl             string
	LastRefreshed          string
	RefreshIntervalMinutes int
	Version                string
	RateLimitedUntil       string
	// Degradations is what elly currently cannot do. Rendered in the meta bar
	// beside the rate-limit notice, so a deficiency is visible on the page
	// rather than only in the log or to whoever thinks to ask the API. Token
	// limitations are left out; see pageDegradations.
	Degradations []types.Degradation
	SetupMode    bool
}

// pageDegradations leaves out token limitations: a warning on every page view
// about something only a different token type fixes is noise. The API still
// reports them, so a consumer never mistakes missing check names for none.
func pageDegradations(ds []types.Degradation) []types.Degradation {
	var out []types.Degradation
	for _, d := range ds {
		if !d.TokenLimitation() {
			out = append(out, d)
		}
	}
	return out
}

//go:embed index.html next.html
var index embed.FS

type HttpServerConfig struct {
	Url            string
	Store          storage.Storage
	TimeoutMinutes int
	Version        string
	Logger         *slog.Logger
	Tracker        *backoff.Tracker
	SetupMode      bool // True if no PAT configured (initial state only)
	// AnnotationToken is the bearer token annotation writes must carry. Empty
	// disables them.
	AnnotationToken string
	// AnnotationSchemes are the link schemes allowed besides https.
	AnnotationSchemes []string
	// GithubURL is where a pasted PAT is checked; empty means GitHub's API.
	GithubURL string
}

func (c HttpServerConfig) githubURL() string {
	if c.GithubURL == "" {
		return github.DefaultAPIURL
	}
	return c.GithubURL
}

func (c HttpServerConfig) logger() *slog.Logger {
	if c.Logger == nil {
		return slog.Default()
	}
	return c.Logger
}

// getCurrentUsername returns the username from the stored PAT, or empty string if not configured.
// prsAtOrAbove returns the PRs scoring at least minimumPoints, highest first,
// most recently updated first on a tie.
func prsAtOrAbove(prs []types.ViewPr, currentUser string, minimumPoints int, now time.Time) []types.ViewPr {
	pointsPerPrUrl := make(map[string]*points.Points)
	for _, pr := range prs {
		pointsPerPrUrl[pr.Url] = points.StandardPrPoints(pr, currentUser, now)
	}

	prsToReturn := make([]types.ViewPr, 0)
	for _, pr := range prs {
		if pointsPerPrUrl[pr.Url].Total >= minimumPoints {
			prsToReturn = append(prsToReturn, pr)
		}
	}

	sort.Slice(prsToReturn, func(i, j int) bool {
		pri := pointsPerPrUrl[prsToReturn[i].Url].Total
		prj := pointsPerPrUrl[prsToReturn[j].Url].Total
		if pri == prj {
			return prsToReturn[j].LastUpdated.Before(prsToReturn[i].LastUpdated)
		}
		return pri > prj
	})
	return prsToReturn
}

func getCurrentUsername(store storage.Storage) string {
	storedPat, found, _ := store.GetPAT()
	if !found {
		return ""
	}
	return storedPat.Username
}

// parseIndex parses the page template. PR titles, authors and repo names are
// written by whoever opens a PR that involves the user, so the template must
// escape them.
func parseIndex(annotationSchemes []string) (*template.Template, error) {
	return template.New("index.html").Funcs(template.FuncMap{
		"annotationHref": annotationHref(annotationSchemes),
		"annotationVerb": annotationVerb,
	}).ParseFS(index, "index.html")
}

// parseNext parses the /next page. Every link on it is checked in Go before
// it reaches the template (see buildNext), and PR fields are escaped by
// html/template like on the classic page.
func parseNext() (*template.Template, error) {
	return template.New("next.html").ParseFS(index, "next.html")
}

func ServeWeb(webConfig HttpServerConfig) {
	mux := routes(webConfig)
	webConfig.Logger.Info("starting web server at", slog.String("url", "http://"+webConfig.Url))
	serverErr := http.ListenAndServe(webConfig.Url, guard(webConfig.Url, mux))
	check(serverErr)
}

// routes registers every handler on a mux of its own, so tests can build it.
func routes(webConfig HttpServerConfig) *http.ServeMux {
	mux := http.NewServeMux()
	temp, err := parseIndex(webConfig.AnnotationSchemes)
	check(err)
	nextTemp, err := parseNext()
	check(err)

	registerAnnotationRoutes(mux, webConfig)

	mux.HandleFunc("POST /api/v0/prs/{prUrl}/{action}", func(w http.ResponseWriter, r *http.Request) {
		prUrlBytes, err := base64.StdEncoding.DecodeString(r.PathValue("prUrl"))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, "invalid PR ID") //nolint:errcheck // best-effort response body
			return
		}
		ghPrUrl := string(prUrlBytes)

		action := r.PathValue("action")

		var buryFunc func(string) error
		switch action {
		case "bury":
			buryFunc = webConfig.Store.Bury
		case "unbury":
			buryFunc = webConfig.Store.Unbury
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprintf(w, "action '%s' is not supported", action) //nolint:errcheck // best-effort response body
			return
		}

		if err := buryFunc(ghPrUrl); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, "couldn't toggle bury for PR %s", ghPrUrl) //nolint:errcheck // best-effort response body
			return
		}

		w.WriteHeader(http.StatusNoContent)
	})

	// Let's say that v0 represents "may change at any time", read the code.
	// Should be bumped before tagging this repo as v1
	mux.HandleFunc("GET /api/v0/prs", func(w http.ResponseWriter, r *http.Request) {
		minimumPoints := -999
		if minPoints := r.URL.Query().Get("minPoints"); minPoints != "" {
			if min, err := strconv.Atoi(minPoints); err == nil && min >= -999 && min <= 999 {
				minimumPoints = min
			}
		}

		prsToReturn := prsAtOrAbove(webConfig.Store.Prs().Prs, getCurrentUsername(webConfig.Store), minimumPoints, time.Now())

		w.Header().Set("Content-Type", "application/json")
		err := json.NewEncoder(w).Encode(prsToReturn)
		check(err)
	})

	mux.HandleFunc("POST /api/v0/prs/refresh", func(w http.ResponseWriter, r *http.Request) {
		webConfig.Tracker.RequestRefresh()
	})

	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		storedPrs := webConfig.Store.Prs()
		prs_ := storedPrs.Prs

		// Check if PAT is configured dynamically
		_, found, _ := webConfig.Store.GetPAT()
		setupMode := !found
		currentUser := getCurrentUsername(webConfig.Store)

		pointsPerPrUrl := make(map[string]*points.Points)
		for _, pr := range prs_ {
			pointsPerPrUrl[pr.Url] = points.StandardPrPoints(pr, currentUser, time.Now())
		}

		sort.Slice(prs_, func(i, j int) bool {
			pri := pointsPerPrUrl[prs_[i].Url].Total
			prj := pointsPerPrUrl[prs_[j].Url].Total
			if pri == prj {
				lastUpdated := prs_[j].LastUpdated.Before(prs_[i].LastUpdated)
				return lastUpdated
			}
			return pri > prj
		})
		rateLimitUntil := webConfig.Store.GetRateLimitUntil()
		rateLimitUntilStr := ""
		if !rateLimitUntil.IsZero() {
			rateLimitUntilStr = rateLimitUntil.Format(time.RFC3339)
		}
		data := IndexHtmlData{
			Prs:                    prs_,
			PointsPerPrUrl:         pointsPerPrUrl,
			CurrentUser:            currentUser,
			LastRefreshed:          storedPrs.LastFetched.Format(time.RFC3339),
			RefreshIntervalMinutes: webConfig.TimeoutMinutes,
			Version:                webConfig.Version,
			RateLimitedUntil:       rateLimitUntilStr,
			Degradations:           pageDegradations(webConfig.Store.Degradations()),
			SetupMode:              setupMode,
		}
		err := temp.Execute(w, data)
		check(err)
	})

	mux.HandleFunc("GET /next", func(w http.ResponseWriter, r *http.Request) {
		storedPrs := webConfig.Store.Prs()
		_, found, _ := webConfig.Store.GetPAT()
		data := buildNext(storedPrs.Prs, getCurrentUsername(webConfig.Store), time.Now(), webConfig.AnnotationSchemes)
		data.LastRefreshed = storedPrs.LastFetched.Format(time.RFC3339)
		data.RefreshIntervalMinutes = webConfig.TimeoutMinutes
		data.Degradations = pageDegradations(webConfig.Store.Degradations())
		data.SetupMode = !found
		if until := webConfig.Store.GetRateLimitUntil(); until.After(time.Now()) {
			data.RateLimitedUntil = until.Format(time.RFC3339)
		}
		check(nextTemp.Execute(w, data))
	})

	mux.Handle("GET /metrics", promhttp.Handler())

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok") //nolint:errcheck // best-effort response body
	})

	mux.HandleFunc("PUT /api/v0/config/pat", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Token string `json:"token"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid JSON"})
			return
		}

		if req.Token == "" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "token is required"})
			return
		}

		// Validate the token with GitHub (checks both authentication and required scopes)
		username, expiresAt, err := github.ValidatePAT(webConfig.githubURL(), req.Token, webConfig.Logger)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid token: " + err.Error()})
			return
		}

		// Store in SQLite
		if err := webConfig.Store.StorePAT(req.Token, username, expiresAt); err != nil {
			webConfig.Logger.Error("could not store PAT", slog.Any("error", err))
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "could not store token"})
			return
		}

		// Trigger a refresh so the new PAT is used immediately
		webConfig.Tracker.RequestRefresh()

		w.Header().Set("Content-Type", "application/json")
		response := map[string]any{
			"username": username,
		}
		if !expiresAt.IsZero() {
			response["expires_at"] = expiresAt.Format(time.RFC3339)
		}
		_ = json.NewEncoder(w).Encode(response)
	})

	mux.HandleFunc("GET /api/v0/config/status", func(w http.ResponseWriter, r *http.Request) {
		storedPat, found, _ := webConfig.Store.GetPAT()
		if !found {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"configured": false,
			})
			return
		}

		w.Header().Set("Content-Type", "application/json")
		// Degradations are reported here rather than left to be inferred from
		// data that merely looks empty. "checks_unreadable" is the case that
		// prompted it: without the Checks read permission a red PR comes back
		// with no failing-check names, which is indistinguishable from a PR
		// with nothing failing unless elly says which it is.
		degradations := make([]map[string]string, 0)
		for _, d := range webConfig.Store.Degradations() {
			degradations = append(degradations, map[string]string{
				"kind":    d.Kind,
				"message": d.Message,
				"remedy":  d.Remedy,
				"seen":    d.Seen.Format(time.RFC3339),
			})
		}
		response := map[string]any{
			"configured":   true,
			"username":     storedPat.Username,
			"stored_at":    storedPat.SetAt.Format(time.RFC3339),
			"degradations": degradations,
		}
		if !storedPat.ExpiresAt.IsZero() {
			response["expires_at"] = storedPat.ExpiresAt.Format(time.RFC3339)
		}
		_ = json.NewEncoder(w).Encode(response)
	})

	mux.HandleFunc("DELETE /api/v0/config/pat", func(w http.ResponseWriter, r *http.Request) {
		if err := webConfig.Store.ClearPAT(); err != nil {
			webConfig.Logger.Error("could not clear PAT", slog.Any("error", err))
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "could not clear token"})
			return
		}

		webConfig.Tracker.Stop()

		w.WriteHeader(http.StatusNoContent)
	})

	return mux
}
