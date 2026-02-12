package backoff

import (
	"log/slog"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	pollIntervalSeconds = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "elly_poll_interval_seconds",
		Help: "Current polling interval in seconds (increases during backoff).",
	})

	backoffMultiplierGauge = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "elly_backoff_multiplier",
		Help: "Current backoff multiplier (1.0 = normal, >1.0 = backing off).",
	})
)

// Tracker owns the polling timer and handles all GitHub fetch outcome
// side effects: metrics, logging, and adaptive backoff.
//
// External code receives "time to poll" signals by calling Tick(), which
// blocks until the next signal (or Stop). Manual refreshes are requested
// via RequestRefresh().
type Tracker struct {
	mu                sync.Mutex
	logger            *slog.Logger
	baseInterval      time.Duration
	multiplier        float64
	maxMultiplier     float64
	consecutiveOK     int
	cooldownThreshold int

	c       chan struct{} // "time to refresh" signals
	refresh chan struct{} // manual refresh requests
	done    chan struct{} // closed by Stop()
	stopped sync.Once
}

func New(logger *slog.Logger, baseInterval time.Duration) *Tracker {
	t := &Tracker{
		logger:            logger,
		baseInterval:      baseInterval,
		multiplier:        1.0,
		maxMultiplier:     4.0,
		cooldownThreshold: 3,
		c:                 make(chan struct{}, 1),
		refresh:           make(chan struct{}, 1),
		done:              make(chan struct{}),
	}
	pollIntervalSeconds.Set(baseInterval.Seconds())
	backoffMultiplierGauge.Set(1.0)
	go t.run()
	return t
}

// Tick blocks until it is time to refresh, then returns true.
// Returns false when Stop() has been called (the tracker is done).
func (t *Tracker) Tick() bool {
	v, ok := <-t.c
	if !ok {
		return false
	}
	_ = v
	return true
}

// RequestRefresh triggers a non-blocking refresh signal.
func (t *Tracker) RequestRefresh() {
	select {
	case t.refresh <- struct{}{}:
	default: // already pending
	}
}

// Stop stops the ticker goroutine and closes the Tick channel.
func (t *Tracker) Stop() {
	t.stopped.Do(func() {
		close(t.done)
	})
}

// RateLimited handles a rate limit response: 2x backoff, timer reset, log.
func (t *Tracker) RateLimited() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.consecutiveOK = 0
	t.multiplier *= 2
	if t.multiplier > t.maxMultiplier {
		t.multiplier = t.maxMultiplier
	}
	t.syncGauges()
	t.logger.Warn("rate limited by github, backing off",
		slog.Duration("interval", t.currentIntervalLocked()))
}

// ServerErrored handles a server error: 1.5x backoff, timer reset, log.
func (t *Tracker) ServerErrored() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.consecutiveOK = 0
	t.multiplier *= 1.5
	if t.multiplier > t.maxMultiplier {
		t.multiplier = t.maxMultiplier
	}
	t.syncGauges()
	t.logger.Warn("server error from github, backing off",
		slog.Duration("interval", t.currentIntervalLocked()))
}

// Succeeded handles a successful fetch and may reduce backoff.
func (t *Tracker) Succeeded() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.consecutiveOK++
	if t.consecutiveOK >= t.cooldownThreshold && t.multiplier > 1.0 {
		t.multiplier /= 2
		if t.multiplier < 1.0 {
			t.multiplier = 1.0
		}
		t.consecutiveOK = 0
	}
	t.syncGauges()
}

// BaseInterval returns the configured base polling interval (before backoff).
func (t *Tracker) BaseInterval() time.Duration {
	return t.baseInterval
}

// currentInterval returns the current effective polling interval.
func (t *Tracker) currentInterval() time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.currentIntervalLocked()
}

// currentIntervalLocked returns the interval. Must be called with mu held.
func (t *Tracker) currentIntervalLocked() time.Duration {
	return time.Duration(float64(t.baseInterval) * t.multiplier)
}

// syncGauges updates Prometheus gauges. Must be called with mu held.
func (t *Tracker) syncGauges() {
	backoffMultiplierGauge.Set(t.multiplier)
	pollIntervalSeconds.Set(t.currentIntervalLocked().Seconds())
}

func (t *Tracker) run() {
	defer close(t.c)

	// Send immediate first signal.
	select {
	case t.c <- struct{}{}:
	case <-t.done:
		return
	}

	timer := time.NewTimer(t.currentInterval())
	defer timer.Stop()

	for {
		select {
		case <-timer.C:
			select {
			case t.c <- struct{}{}:
			default: // don't block if pending
			}
			timer.Reset(t.currentInterval())
		case <-t.refresh:
			// Drain timer so the next tick is a full interval from now.
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			select {
			case t.c <- struct{}{}:
			default:
			}
			timer.Reset(t.currentInterval())
		case <-t.done:
			return
		}
	}
}
