package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/chelmertz/elly/internal/points"
	"github.com/chelmertz/elly/internal/types"
	"pgregory.net/rapid"
)

func TestPropGuardRefusesEveryForeignHost(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	g := guard("localhost:9876", ok)
	rapid.Check(t, func(t *rapid.T) {
		name := rapid.OneOf(rapid.SampledFrom([]string{"localhost", "127.0.0.1", "127.1.2.3", "localhost.evil.example"}), rapid.StringMatching(`[a-z0-9.-]{1,20}`)).Draw(t, "name")
		// Mostly the listen port, or the port check alone hides the name check.
		port := rapid.OneOf(rapid.Just(9876), rapid.IntRange(1, 65535)).Draw(t, "port")
		host := fmt.Sprintf("%s:%d", name, port)
		r := httptest.NewRequest("GET", "/", nil)
		r.Host = host
		w := httptest.NewRecorder()
		g.ServeHTTP(w, r)
		allowed := port == 9876 && (name == "localhost" || strings.HasPrefix(name, "127.") && isLoopback(name))
		if (w.Code == http.StatusNoContent) != allowed {
			t.Fatalf("Host %q got %d", host, w.Code)
		}
	})
}

func TestPropGuardRefusesEveryCrossSiteWrite(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	g := guard("localhost:9876", ok)
	rapid.Check(t, func(t *rapid.T) {
		method := rapid.SampledFrom([]string{"POST", "PUT", "DELETE", "PATCH"}).Draw(t, "method")
		origin := "https://" + rapid.StringMatching(`[a-z]{1,10}\.example`).Draw(t, "site")
		r := httptest.NewRequest(method, "/", nil)
		r.Host = "localhost:9876"
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		g.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s from %s got %d", method, origin, w.Code)
		}
	})
}

func TestPropAnnotationExpiryBounds(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		ttl := time.Duration(rapid.Int64Range(-int64(48*time.Hour), int64(48*time.Hour)).Draw(t, "ttl")).Truncate(time.Second)
		b := `{"state":"handled","expiresAt":"` + testNow.Add(ttl).Format(time.RFC3339) + `"}`
		_, _, err := validateAnnotation("t", strings.NewReader(b), nil, testNow)
		want := ttl >= minAnnotationTTL && ttl <= maxAnnotationTTL
		if (err == nil) != want {
			t.Fatalf("ttl %v: err %v", ttl, err)
		}
	})
}

func TestPropOnlyAllowlistedSchemesPass(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		scheme := rapid.StringMatching(`[a-z][a-z0-9+.-]{0,12}`).Draw(t, "scheme")
		allowed := rapid.SliceOfN(rapid.StringMatching(`[a-z]{1,8}`), 0, 3).Draw(t, "allowed")
		raw := scheme + "://host/path"
		want := scheme == "https" || slices.Contains(allowed, scheme)
		if got := allowedLink(raw, allowed); got != want {
			t.Fatalf("%q with %v: got %v", raw, allowed, got)
		}
		if href := string(annotationHref(allowed)(raw)); (href != "#") != want {
			t.Fatalf("rendered %q as %q", raw, href)
		}
	})
}

func TestPropPrsAtOrAboveIsASortedFilteredSubset(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		now := time.Unix(1_800_000_000, 0)
		n := rapid.IntRange(0, 8).Draw(t, "n")
		prs := make([]types.ViewPr, n)
		for i := range prs {
			prs[i] = types.ViewPr{
				Url:          fmt.Sprint(i),
				Author:       "other",
				Additions:    rapid.IntRange(0, 500).Draw(t, "add"),
				ReviewStatus: rapid.SampledFrom([]string{"", "APPROVED"}).Draw(t, "status"),
				LastUpdated:  now.Add(-time.Duration(rapid.IntRange(0, 100).Draw(t, "age")) * time.Hour),
			}
		}
		min := rapid.IntRange(-200, 100).Draw(t, "min")
		got := prsAtOrAbove(prs, "me", min, now)

		prev := 1 << 30
		for _, p := range got {
			total := points.StandardPrPoints(p, "me", now).Total
			if total < min || total > prev {
				t.Fatalf("unsorted or below %d: %d after %d", min, total, prev)
			}
			prev = total
		}
		want := 0
		for _, p := range prs {
			if points.StandardPrPoints(p, "me", now).Total >= min {
				want++
			}
		}
		if len(got) != want {
			t.Fatalf("kept %d of %d qualifying PRs", len(got), want)
		}
	})
}
