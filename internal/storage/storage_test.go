package storage

import (
	"io"
	"testing"
	"time"

	"log/slog"
)

func setupTestStorage(t *testing.T) *DbStorage {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store := NewStorage(logger, ":memory:")
	return store
}

func TestStorePAT_InsertsNewPATAsActive(t *testing.T) {
	store := setupTestStorage(t)

	token := "ghp_test123"
	username := "testuser"
	expiresAt := time.Now().Add(30 * 24 * time.Hour)

	err := store.StorePAT(token, username, expiresAt)
	if err != nil {
		t.Fatalf("StorePAT failed: %v", err)
	}

	got, found, err := store.GetPAT()
	if err != nil {
		t.Fatalf("GetPAT failed: %v", err)
	}
	if !found {
		t.Fatal("expected PAT to be found")
	}

	if got.Token != token {
		t.Errorf("expected token %q, got %q", token, got.Token)
	}
	if got.Username != username {
		t.Errorf("expected username %q, got %q", username, got.Username)
	}
}

func TestStorePAT_DeactivatesPreviousPAT(t *testing.T) {
	store := setupTestStorage(t)

	// Store first PAT
	err := store.StorePAT("first_token", "user1", time.Time{})
	if err != nil {
		t.Fatalf("first StorePAT failed: %v", err)
	}

	// Store second PAT
	err = store.StorePAT("second_token", "user2", time.Time{})
	if err != nil {
		t.Fatalf("second StorePAT failed: %v", err)
	}

	// Get PAT should return only the second one
	got, found, err := store.GetPAT()
	if err != nil {
		t.Fatalf("GetPAT failed: %v", err)
	}
	if !found {
		t.Fatal("expected PAT to be found")
	}

	if got.Token != "second_token" {
		t.Errorf("expected token %q, got %q", "second_token", got.Token)
	}
	if got.Username != "user2" {
		t.Errorf("expected username %q, got %q", "user2", got.Username)
	}
}

func TestGetPAT_ReturnsNotFoundWhenNoPAT(t *testing.T) {
	store := setupTestStorage(t)

	_, found, err := store.GetPAT()
	if err != nil {
		t.Fatalf("GetPAT returned unexpected error: %v", err)
	}
	if found {
		t.Fatal("expected found=false when no PAT configured")
	}
}

func TestClearPAT_DeactivatesActivePAT(t *testing.T) {
	store := setupTestStorage(t)

	// Store a PAT
	err := store.StorePAT("test_token", "testuser", time.Time{})
	if err != nil {
		t.Fatalf("StorePAT failed: %v", err)
	}

	// Verify it exists
	_, found, err := store.GetPAT()
	if err != nil {
		t.Fatalf("GetPAT failed before clear: %v", err)
	}
	if !found {
		t.Fatal("expected PAT to be found before clear")
	}

	// Clear the PAT
	err = store.ClearPAT()
	if err != nil {
		t.Fatalf("ClearPAT failed: %v", err)
	}

	// Verify it's gone
	_, found, err = store.GetPAT()
	if err != nil {
		t.Fatalf("GetPAT returned unexpected error after clear: %v", err)
	}
	if found {
		t.Fatal("expected found=false after clearing PAT")
	}
}

func TestStorePAT_HandlesNonExpiringToken(t *testing.T) {
	store := setupTestStorage(t)

	// Store with zero expiration time (non-expiring token)
	err := store.StorePAT("non_expiring_token", "testuser", time.Time{})
	if err != nil {
		t.Fatalf("StorePAT failed: %v", err)
	}

	got, found, err := store.GetPAT()
	if err != nil {
		t.Fatalf("GetPAT failed: %v", err)
	}
	if !found {
		t.Fatal("expected PAT to be found")
	}

	if !got.ExpiresAt.IsZero() {
		t.Errorf("expected zero expiration time for non-expiring token, got %v", got.ExpiresAt)
	}
}

func TestStorePAT_PreservesExpirationTime(t *testing.T) {
	store := setupTestStorage(t)

	expiresAt := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	err := store.StorePAT("expiring_token", "testuser", expiresAt)
	if err != nil {
		t.Fatalf("StorePAT failed: %v", err)
	}

	got, found, err := store.GetPAT()
	if err != nil {
		t.Fatalf("GetPAT failed: %v", err)
	}
	if !found {
		t.Fatal("expected PAT to be found")
	}

	// Compare with second precision (RFC3339 doesn't preserve nanoseconds)
	if !got.ExpiresAt.Truncate(time.Second).Equal(expiresAt.Truncate(time.Second)) {
		t.Errorf("expected expiration %v, got %v", expiresAt, got.ExpiresAt)
	}
}
