package store

import (
	"errors"
	"testing"
	"time"
)

func TestRegistrationAndSessionLifecycle(t *testing.T) {
	database, ctx, now := openTestStore(t)

	enabled, err := database.RegistrationEnabled(ctx, false)
	if err != nil || !enabled {
		t.Fatalf("initial registration enabled = %v, error = %v", enabled, err)
	}
	user, err := database.RegisterUser(ctx, "alice", "first-hash", false, now)
	if err != nil {
		t.Fatal(err)
	}
	if user.Username != "alice" || user.PasswordHash != "first-hash" {
		t.Fatalf("registered user = %#v", user)
	}

	enabled, err = database.RegistrationEnabled(ctx, false)
	if err != nil || enabled {
		t.Fatalf("registration after first user = %v, error = %v", enabled, err)
	}
	if _, err := database.RegisterUser(ctx, "bob", "second-hash", false, now); !errors.Is(err, ErrRegistrationDisabled) {
		t.Fatalf("second registration error = %v", err)
	}

	expires := now.Add(time.Hour)
	if err := database.CreateSession(ctx, "token-one", "alice", now, expires); err != nil {
		t.Fatal(err)
	}
	if _, err := database.SessionUser(ctx, "token-one", now.Add(time.Minute), now.Add(2*time.Hour)); err != nil {
		t.Fatalf("valid session lookup: %v", err)
	}
	if err := database.UpdatePassword(ctx, "alice", "new-hash", now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := database.SessionUser(ctx, "token-one", now.Add(3*time.Minute), now.Add(2*time.Hour)); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("revoked session error = %v", err)
	}
	user, err = database.User(ctx, "alice")
	if err != nil || user.PasswordHash != "new-hash" || user.PasswordChangedAt == nil {
		t.Fatalf("updated user = %#v, error = %v", user, err)
	}
}

func TestExpiredSessionIsRejected(t *testing.T) {
	database, ctx, now := openTestStore(t)
	if _, err := database.RegisterUser(ctx, "alice", "hash", false, now); err != nil {
		t.Fatal(err)
	}
	if err := database.CreateSession(ctx, "expired", "alice", now.Add(-2*time.Hour), now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := database.SessionUser(ctx, "expired", now, now.Add(time.Hour)); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("expired session error = %v", err)
	}
}
