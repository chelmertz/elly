package server

import (
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"text/template"
	"time"

	"github.com/chelmertz/elly/internal/github"
	"github.com/chelmertz/elly/internal/points"
	"github.com/chelmertz/elly/internal/storage"
	"github.com/chelmertz/elly/internal/types"
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
	GoldenTestingEnabled   bool
	RateLimitedUntil       string
	SetupMode              bool
}

//go:embed index.html
var index embed.FS

type HttpServerConfig struct {
	Url                  string
	GoldenTestingEnabled bool
	Store                storage.Storage
	TimeoutMinutes       int
	Version              string
	Logger               *slog.Logger
	RefreshingChannel    chan types.RefreshAction
	SetupMode            bool // True if no PAT configured (initial state only)
}

// getCurrentUsername returns the username from the stored PAT, or empty string if not configured.
func getCurrentUsername(store storage.Storage) string {
	storedPat, found, _ := store.GetPAT()
	if !found {
		return ""
	}
	return storedPat.Username
}

func ServeWeb(webConfig HttpServerConfig) {
	temp, err := template.ParseFS(index, "index.html")
	check(err)

	http.HandleFunc("POST /api/v0/prs/{prUrl}/golden", func(w http.ResponseWriter, r *http.Request) {
		if !webConfig.GoldenTestingEnabled {
			// Nothing to see here, the feature is turned off - restart with -golden to turn it on
			w.WriteHeader(http.StatusNotFound)
			return
		}

		storedPrs := webConfig.Store.Prs()
		prs_ := storedPrs.Prs

		prUrlBytes, err := base64.StdEncoding.DecodeString(r.PathValue("prUrl"))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, "invalid PR ID") //nolint:errcheck // best-effort response body
			return
		}
		ghPrUrl := string(prUrlBytes)

		var foundPr types.ViewPr
		found := false // just to avoid dealing with an empty state PR, or mucking with a pointer
		for _, pr := range prs_ {
			if ghPrUrl == pr.Url {
				// not the most efficient but I've never had more than ~30 PRs showing
				// query the DB if this ever gets expensive
				foundPr = pr
				found = true
				break
			}
		}
		if !found {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprintf(w, "couldn't turn PR %s into a golden copy, PR not found", ghPrUrl) //nolint:errcheck // best-effort response body
			return
		}

		webConfig.Logger.Info("found a pr to turn into golden copy", "pr", foundPr)

		now := time.Now()
		currentUser := getCurrentUsername(webConfig.Store)
		points.StoreGoldenTest(points.GoldenTest{
			PrDomain:    foundPr,
			Points:      *points.StandardPrPoints(foundPr, currentUser, now),
			CurrentUser: currentUser,
			CurrentTime: now,
		})
		w.WriteHeader(http.StatusOK)
	})

	http.HandleFunc("POST /api/v0/prs/{prUrl}/{action}", func(w http.ResponseWriter, r *http.Request) {
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
	http.HandleFunc("GET /api/v0/prs", func(w http.ResponseWriter, r *http.Request) {
		storedPrs := webConfig.Store.Prs().Prs
		prsToReturn := make([]types.ViewPr, 0)

		minimumPoints := -999
		if minPoints := r.URL.Query().Get("minPoints"); minPoints != "" {
			if min, err := strconv.Atoi(minPoints); err == nil && min >= -999 && min <= 999 {
				minimumPoints = min
			}
		}

		currentUser := getCurrentUsername(webConfig.Store)
		pointsPerPrUrl := make(map[string]*points.Points)
		for _, pr := range storedPrs {
			points := points.StandardPrPoints(pr, currentUser, time.Now())
			pointsPerPrUrl[pr.Url] = points
		}

		// TODO turn into slices.DeleteFunc()
		for _, pr := range storedPrs {
			points := pointsPerPrUrl[pr.Url]
			if points.Total >= minimumPoints {
				prsToReturn = append(prsToReturn, pr)
			}
		}

		sort.Slice(prsToReturn, func(i, j int) bool {
			pri := pointsPerPrUrl[storedPrs[i].Url].Total
			prj := pointsPerPrUrl[storedPrs[j].Url].Total
			if pri == prj {
				lastUpdated := storedPrs[j].LastUpdated.Before(storedPrs[i].LastUpdated)
				return lastUpdated
			}
			return pri > prj
		})

		w.Header().Set("Content-Type", "application/json")
		err := json.NewEncoder(w).Encode(prsToReturn)
		check(err)
	})

	http.HandleFunc("POST /api/v0/prs/refresh", func(w http.ResponseWriter, r *http.Request) {
		webConfig.RefreshingChannel <- types.RefreshManual
	})

	http.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
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
			GoldenTestingEnabled:   webConfig.GoldenTestingEnabled,
			RateLimitedUntil:       rateLimitUntilStr,
			SetupMode:              setupMode,
		}
		err := temp.Execute(w, data)
		check(err)
	})

	http.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok") //nolint:errcheck // best-effort response body
	})

	http.HandleFunc("PUT /api/v0/config/pat", func(w http.ResponseWriter, r *http.Request) {
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

		// Validate the token with GitHub
		username, expiresAt, err := github.UsernameFromPat(req.Token, webConfig.Logger)
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
		if webConfig.RefreshingChannel != nil {
			select {
			case webConfig.RefreshingChannel <- types.RefreshManual:
			default:
				// channel full, skip without blocking
			}
		}

		w.Header().Set("Content-Type", "application/json")
		response := map[string]any{
			"username": username,
		}
		if !expiresAt.IsZero() {
			response["expires_at"] = expiresAt.Format(time.RFC3339)
		}
		_ = json.NewEncoder(w).Encode(response)
	})

	http.HandleFunc("GET /api/v0/config/status", func(w http.ResponseWriter, r *http.Request) {
		storedPat, found, _ := webConfig.Store.GetPAT()
		if !found {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"configured": false,
			})
			return
		}

		w.Header().Set("Content-Type", "application/json")
		response := map[string]any{
			"configured": true,
			"username":   storedPat.Username,
			"stored_at":  storedPat.SetAt.Format(time.RFC3339),
		}
		if !storedPat.ExpiresAt.IsZero() {
			response["expires_at"] = storedPat.ExpiresAt.Format(time.RFC3339)
		}
		_ = json.NewEncoder(w).Encode(response)
	})

	http.HandleFunc("DELETE /api/v0/config/pat", func(w http.ResponseWriter, r *http.Request) {
		if err := webConfig.Store.ClearPAT(); err != nil {
			webConfig.Logger.Error("could not clear PAT", slog.Any("error", err))
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "could not clear token"})
			return
		}

		// Signal refresh loop to stop
		if webConfig.RefreshingChannel != nil {
			select {
			case webConfig.RefreshingChannel <- types.RefreshStop:
			default:
				// channel full, skip without blocking
			}
		}

		w.WriteHeader(http.StatusNoContent)
	})

	webConfig.Logger.Info("starting web server at", slog.String("url", "http://"+webConfig.Url))
	serverErr := http.ListenAndServe(webConfig.Url, nil)
	check(serverErr)
}
