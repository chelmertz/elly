package storage

import (
	"sync"

	"github.com/chelmertz/elly/internal/types"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	prsTracked = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "elly_prs_tracked",
		Help: "Number of PRs currently tracked.",
	})

	prsSeenTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "elly_prs_seen_total",
		Help: "Total number of unique PRs seen since startup.",
	})

	seenMu  sync.Mutex
	seenPRs = make(map[string]struct{})
)

func trackPRs(prs []types.ViewPr) {
	seenMu.Lock()
	defer seenMu.Unlock()
	prsTracked.Set(float64(len(prs)))
	for _, pr := range prs {
		if _, seen := seenPRs[pr.Url]; !seen {
			seenPRs[pr.Url] = struct{}{}
			prsSeenTotal.Inc()
		}
	}
}
