package backoff

import (
	"io"
	"log/slog"
	"testing"
	"testing/synctest"
	"time"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestRateLimitedDoublesInterval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		bt := New(discardLogger(), 5*time.Minute)
		defer bt.Stop()

		bt.RateLimited()
		if got, want := bt.currentInterval(), 10*time.Minute; got != want {
			t.Fatalf("after 1 rate limit: got %v, want %v", got, want)
		}

		bt.RateLimited()
		if got, want := bt.currentInterval(), 20*time.Minute; got != want {
			t.Fatalf("after 2 rate limits: got %v, want %v", got, want)
		}

		// Capped at 4x
		bt.RateLimited()
		if got, want := bt.currentInterval(), 20*time.Minute; got != want {
			t.Fatalf("after 3 rate limits (should cap at 4x): got %v, want %v", got, want)
		}
	})
}

func TestServerErroredIncreasesInterval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		bt := New(discardLogger(), 10*time.Minute)
		defer bt.Stop()

		bt.ServerErrored()
		if got, want := bt.currentInterval(), 15*time.Minute; got != want {
			t.Fatalf("after 1 server error: got %v, want %v", got, want)
		}
	})
}

func TestSucceededGraduallyReducesMultiplier(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		bt := New(discardLogger(), 5*time.Minute)
		defer bt.Stop()

		// Back off first
		bt.RateLimited() // 2x = 10min
		bt.RateLimited() // 4x = 20min

		// 2 successes: no reduction yet
		bt.Succeeded()
		bt.Succeeded()
		if got, want := bt.currentInterval(), 20*time.Minute; got != want {
			t.Fatalf("after 2 successes (no reduction yet): got %v, want %v", got, want)
		}

		// 3rd success triggers halving: 4x / 2 = 2x = 10min
		bt.Succeeded()
		if got, want := bt.currentInterval(), 10*time.Minute; got != want {
			t.Fatalf("after 3 successes: got %v, want %v", got, want)
		}

		// 3 more successes: 2x / 2 = 1x = 5min
		bt.Succeeded()
		bt.Succeeded()
		bt.Succeeded()
		if got, want := bt.currentInterval(), 5*time.Minute; got != want {
			t.Fatalf("after 6 total successes: got %v, want %v", got, want)
		}
	})
}

func TestSucceededDoesNotGoBelowBase(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		bt := New(discardLogger(), 5*time.Minute)
		defer bt.Stop()

		for range 10 {
			bt.Succeeded()
		}
		if got, want := bt.currentInterval(), 5*time.Minute; got != want {
			t.Fatalf("should stay at base: got %v, want %v", got, want)
		}
	})
}

func TestRateLimitResetsConsecutiveSuccesses(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		bt := New(discardLogger(), 5*time.Minute)
		defer bt.Stop()

		bt.RateLimited() // 2x
		bt.Succeeded()   // 1 consecutive
		bt.Succeeded()   // 2 consecutive
		bt.RateLimited() // resets consecutive count, 4x

		// These 2 successes shouldn't trigger a halving since count was reset
		bt.Succeeded()
		bt.Succeeded()
		if got, want := bt.currentInterval(), 20*time.Minute; got != want {
			t.Fatalf("consecutive count should have been reset: got %v, want %v", got, want)
		}
	})
}

func TestTickDeliversSignalAndStopCloses(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		bt := New(discardLogger(), 5*time.Minute)

		// First Tick should return immediately (initial signal).
		if !bt.Tick() {
			t.Fatal("first Tick() should return true")
		}

		// Advance past the interval so the timer fires.
		time.Sleep(6 * time.Minute)
		if !bt.Tick() {
			t.Fatal("Tick() after timer should return true")
		}

		bt.Stop()
		if bt.Tick() {
			t.Fatal("Tick() after Stop() should return false")
		}
	})
}

func TestRequestRefreshDeliversTick(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		bt := New(discardLogger(), 1*time.Hour)

		// Consume the initial signal.
		if !bt.Tick() {
			t.Fatal("first Tick() should return true")
		}

		// Manual refresh should deliver without waiting for the long timer.
		bt.RequestRefresh()
		if !bt.Tick() {
			t.Fatal("Tick() after RequestRefresh should return true")
		}

		bt.Stop()
	})
}

func TestTimerResetsOnBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := 5 * time.Minute
		bt := New(discardLogger(), base)

		// Consume initial signal.
		if !bt.Tick() {
			t.Fatal("first Tick() should return true")
		}

		// Rate limit doubles interval to 10min.
		bt.RateLimited()

		// At 6 minutes (past original 5min), no tick should be ready yet
		// because the timer inside run() was set at New() time and will
		// fire at 5min, but then reset to 10min. We need to consume that
		// 5min tick first, then verify the next one takes 10min.
		time.Sleep(6 * time.Minute)
		// The timer fired at 5min, so there should be a signal.
		if !bt.Tick() {
			t.Fatal("Tick() should return true (timer from before backoff)")
		}

		// Now the timer was reset to 10min. At 6min, nothing yet.
		time.Sleep(6 * time.Minute)
		done := make(chan bool, 1)
		go func() {
			done <- bt.Tick()
		}()

		select {
		case <-done:
			t.Fatal("ticker should not fire before new 10min interval")
		default:
		}

		// At 10 minutes total from the reset, it should fire.
		time.Sleep(4 * time.Minute)
		result := <-done
		if !result {
			t.Fatal("Tick() should have returned true at 10min interval")
		}

		bt.Stop()
	})
}
