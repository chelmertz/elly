package server

import (
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Changes tells open pages that something they show changed, so they
// re-render instead of reloading on a timer. storage.Notifying calls
// Changed on every write the page shows; GET /api/v0/events streams the
// version.
type Changes struct {
	mu   sync.Mutex
	boot int64 // start time, so a restarted elly never repeats a version
	n    uint64
	subs map[chan struct{}]struct{}
}

func NewChanges() *Changes {
	return &Changes{boot: time.Now().UnixNano(), subs: map[chan struct{}]struct{}{}}
}

// Version names the current state; it differs after every change and
// across restarts.
func (c *Changes) Version() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return fmt.Sprintf("%d-%d", c.boot, c.n)
}

// Changed moves the version and signals every subscriber. A subscriber that
// has not looked yet keeps one pending signal: changes coalesce.
func (c *Changes) Changed() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
	for ch := range c.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// Subscribe returns a channel signalled after changes, and its cancel.
func (c *Changes) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	c.mu.Lock()
	c.subs[ch] = struct{}{}
	c.mu.Unlock()
	return ch, func() {
		c.mu.Lock()
		delete(c.subs, ch)
		c.mu.Unlock()
	}
}

// eventsKeepalive is under any proxy's idle cut and the write deadline.
const eventsKeepalive = 25 * time.Second

// serveEvents streams the version as server-sent events: the current one
// at once, then one per change, with a comment line as keepalive. The
// server's write timeout would end the stream, so each write extends its
// own deadline.
func (c *Changes) serveEvents(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	ch, cancel := c.Subscribe()
	defer cancel()
	send := func(line string) bool {
		_ = rc.SetWriteDeadline(time.Now().Add(2 * eventsKeepalive))
		if _, err := fmt.Fprint(w, line); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	if !send("data: " + c.Version() + "\n\n") {
		return
	}
	keepalive := time.NewTicker(eventsKeepalive)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ch:
			if !send("data: " + c.Version() + "\n\n") {
				return
			}
		case <-keepalive.C:
			if !send(": keepalive\n\n") {
				return
			}
		}
	}
}
