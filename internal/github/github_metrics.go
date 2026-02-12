package github

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	githubRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "elly_github_requests_total",
		Help: "Total number of outgoing GitHub API requests.",
	}, []string{"result"})

	rateLimitEventsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "elly_rate_limit_events_total",
		Help: "Total number of rate limit events from GitHub.",
	})
)
