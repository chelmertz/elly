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
}

//go:embed index.html
var index embed.FS

func ServeWeb(url, username string, goldenTestingEnabled bool, store storage.Storage, refreshingChannel chan types.RefreshAction, timeoutMinutes int, version string, logger *slog.Logger) {
	temp, err := template.ParseFS(index, "index.html")
	check(err)

	http.HandleFunc("POST /api/v0/prs/{prUrl}/golden", func(w http.ResponseWriter, r *http.Request) {
		if !goldenTestingEnabled {
			// Nothing to see here
			w.WriteHeader(http.StatusNotFound)
			return
		}

		storedPrs := store.Prs()
		prs_ := storedPrs.Prs

		prUrlBytes, err := base64.StdEncoding.DecodeString(r.PathValue("prUrl"))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("invalid PR ID"))
			return
		}
		ghPrUrl := string(prUrlBytes)

		var foundPr types.ViewPr
		found := false // just to avoid dealing with an empty state PR, or mucking with a pointer
		for _, pr := range prs_ {
			if ghPrUrl == pr.Url {
				// not the most effectient but I've never had more than ~30 PRs showing
				// query the DB if this ever gets expensive
				foundPr = pr
				found = true
				break
			}
		}
		if !found {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(fmt.Sprintf("couldn't turn PR %s into a golden copy, PR not found", ghPrUrl)))
			return
		}

		logger.Info("found a pr to turn into golden copy", "pr", foundPr)

		w.WriteHeader(http.StatusOK)
	})

	http.HandleFunc("POST /api/v0/prs/{prUrl}/{action}", func(w http.ResponseWriter, r *http.Request) {
		prUrlBytes, err := base64.StdEncoding.DecodeString(r.PathValue("prUrl"))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("invalid PR ID"))
			return
		}
		ghPrUrl := string(prUrlBytes)

		action := r.PathValue("action")

		var buryFunc func(string) error
		switch action {
		case "bury":
			buryFunc = store.Bury
		case "unbury":
			buryFunc = store.Unbury
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(fmt.Sprintf("action '%s' is not supported", action)))
			return
		}

		if err := buryFunc(ghPrUrl); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(fmt.Sprintf("couldn't toggle bury for PR %s", ghPrUrl)))
			return
		}

		w.WriteHeader(http.StatusNoContent)
	})

	// Let's say that v0 represents "may change at any time", read the code.
	// Should be bumped before tagging this repo as v1
	http.HandleFunc("GET /api/v0/prs", func(w http.ResponseWriter, r *http.Request) {
		storedPrs := store.Prs().Prs
		prsToReturn := make([]types.ViewPr, 0)

		minimumPoints := -999
		if minPoints := r.URL.Query().Get("minPoints"); minPoints != "" {
			if min, err := strconv.Atoi(minPoints); err == nil && min >= -999 && min <= 999 {
				minimumPoints = min
			}
		}

		pointsPerPrUrl := make(map[string]*points.Points)
		for _, pr := range storedPrs {
			points := points.StandardPrPoints(pr, username)
			pointsPerPrUrl[pr.Url] = points
		}

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
		refreshingChannel <- types.RefreshManual
	})

	http.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		storedPrs := store.Prs()
		prs_ := storedPrs.Prs

		pointsPerPrUrl := make(map[string]*points.Points)
		for _, pr := range prs_ {
			pointsPerPrUrl[pr.Url] = points.StandardPrPoints(pr, username)
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
		data := IndexHtmlData{
			Prs:                    prs_,
			PointsPerPrUrl:         pointsPerPrUrl,
			CurrentUser:            username,
			LastRefreshed:          storedPrs.LastFetched.Format(time.RFC3339),
			RefreshIntervalMinutes: timeoutMinutes,
			Version:                version,
		}
		err := temp.Execute(w, data)
		check(err)
	})

	logger.Info("starting web server at", slog.String("url", "http://"+url))
	err = http.ListenAndServe(url, nil)
	check(err)
}
