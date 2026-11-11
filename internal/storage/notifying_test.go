package storage

import (
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/chelmertz/elly/internal/types"
)

// Every Storage method is either a read or a write the page shows. A
// method in neither list fails here, so a new write cannot slip past the
// page's change stream.
var (
	shownWrites = []string{"StoreRepoPrs", "Bury", "Unbury", "SetRateLimitUntil", "StorePAT", "ClearPAT",
		"PutOwnerPAT", "DeleteOwnerPAT", "SetOwnerPollState", "SetPATExpiry", "SetOwnerPATExpiry",
		"SetPATOwner", "StoreDegradations", "PutAnnotation", "DeleteAnnotation"}
	// IsRateLimitActive only clears a limit already past, which the page
	// already hides; StoreNotifySeen is never shown.
	silentMethods = []string{"Prs", "GetPr", "IsRateLimitActive", "GetRateLimitUntil", "GetPAT", "OwnerPATs",
		"Degradations", "StoreNotifySeen", "NotifySeen"}
)

func TestEveryStorageMethodIsClassified(t *testing.T) {
	st := reflect.TypeFor[Storage]()
	for i := range st.NumMethod() {
		name := st.Method(i).Name
		if !slices.Contains(shownWrites, name) && !slices.Contains(silentMethods, name) {
			t.Errorf("Storage.%s is neither a shown write nor silent; classify it here and in Notifying", name)
		}
	}
}

func TestNotifyingBumpsOnEveryShownWrite(t *testing.T) {
	bumps := 0
	s := Notifying(setupTestStorage(t), func() { bumps++ })
	now := time.Now()
	calls := map[string]func(){
		"StorePAT":          func() { _ = s.StorePAT("github_pat_a", "me", now) },
		"PutOwnerPAT":       func() { _ = s.PutOwnerPAT("acme", "github_pat_b", "me", now) },
		"SetOwnerPollState": func() { _ = s.SetOwnerPollState(OwnerPAT{Owner: "acme", Token: "github_pat_b"}) },
		"SetPATExpiry":      func() { _ = s.SetPATExpiry("github_pat_a", now) },
		"SetOwnerPATExpiry": func() { _ = s.SetOwnerPATExpiry("acme", "github_pat_b", now) },
		"SetPATOwner":       func() { _ = s.SetPATOwner("github_pat_a", "me") },
		"StoreRepoPrs":      func() { _ = s.StoreRepoPrs([]types.ViewPr{{Url: "https://github.com/a/b/pull/1"}}) },
		"Bury":              func() { _ = s.Bury("https://github.com/a/b/pull/1") },
		"Unbury":            func() { _ = s.Unbury("https://github.com/a/b/pull/1") },
		"SetRateLimitUntil": func() { _ = s.SetRateLimitUntil(now) },
		"StoreDegradations": func() { _ = s.StoreDegradations(nil) },
		"PutAnnotation": func() {
			_ = s.PutAnnotation("https://github.com/a/b/pull/1", types.Annotation{Source: "x", State: types.AnnotationInfo}, now.Add(time.Hour))
		},
		"DeleteAnnotation": func() { _ = s.DeleteAnnotation("https://github.com/a/b/pull/1", "x") },
		"DeleteOwnerPAT":   func() { _, _ = s.DeleteOwnerPAT("acme") },
		"ClearPAT":         func() { _ = s.ClearPAT() },
	}
	for _, name := range shownWrites {
		call, ok := calls[name]
		if !ok {
			t.Fatalf("no call for %s", name)
		}
		before := bumps
		call()
		if bumps != before+1 {
			t.Errorf("%s bumped %d times, want 1", name, bumps-before)
		}
	}
	before := bumps
	s.Prs()
	_, _, _ = s.GetPAT()
	_ = s.StoreNotifySeen(map[string][]string{})
	s.IsRateLimitActive(now)
	if bumps != before {
		t.Errorf("reads bumped %d times", bumps-before)
	}
}
