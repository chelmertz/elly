package server

import (
	"bufio"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A subscriber hears about a change, several changes before it looks are
// one, and a cancelled subscriber hears nothing.
func TestChangesCoalesceAndCancel(t *testing.T) {
	c := NewChanges()
	ch, cancel := c.Subscribe()
	v0 := c.Version()
	c.Changed()
	c.Changed()
	select {
	case <-ch:
	default:
		t.Fatal("no signal after a change")
	}
	select {
	case <-ch:
		t.Fatal("two changes signalled twice")
	default:
	}
	if c.Version() == v0 {
		t.Error("the version did not move")
	}
	cancel()
	c.Changed()
	select {
	case <-ch:
		t.Error("a cancelled subscriber was signalled")
	default:
	}
}

// Two processes never share a version, so a page that reconnects to a
// restarted elly always re-renders.
func TestChangesVersionDiffersAcrossStarts(t *testing.T) {
	if NewChanges().Version() == func() string { time.Sleep(time.Millisecond); return NewChanges().Version() }() {
		t.Error("two starts share a version")
	}
}

// The stream sends the current version at once and the next one on a
// change.
func TestEventsStreamSendsVersions(t *testing.T) {
	c := NewChanges()
	srv := httptest.NewServer(http.HandlerFunc(c.serveEvents))
	t.Cleanup(srv.Close)
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("content type %q", ct)
	}
	lines := bufio.NewScanner(resp.Body)
	next := func() string {
		for lines.Scan() {
			if v, ok := strings.CutPrefix(lines.Text(), "data: "); ok {
				return v
			}
		}
		t.Fatal("stream ended")
		return ""
	}
	if got := next(); got != c.Version() {
		t.Errorf("first event %q, want %q", got, c.Version())
	}
	c.Changed()
	if got := next(); got != c.Version() {
		t.Errorf("after a change %q, want %q", got, c.Version())
	}
}

// The server's write timeout does not end the stream: each event extends
// its own deadline.
func TestEventsOutliveTheWriteTimeout(t *testing.T) {
	c := NewChanges()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := newHTTPServer("", http.HandlerFunc(c.serveEvents), serverTimeouts{header: time.Second, read: time.Second, write: 100 * time.Millisecond, idle: time.Second})
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	resp, err := http.Get("http://" + ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	lines := bufio.NewScanner(resp.Body)
	events := 0
	for events < 2 && lines.Scan() {
		if strings.HasPrefix(lines.Text(), "data: ") {
			events++
			if events == 1 {
				time.Sleep(300 * time.Millisecond)
				c.Changed()
			}
		}
	}
	if events != 2 {
		t.Errorf("got %d events; the stream ended past the write timeout: %v", events, lines.Err())
	}
}
