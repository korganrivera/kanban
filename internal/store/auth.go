package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

var (
	ErrUserNotFound         = errors.New("user not found")
	ErrUserExists           = errors.New("username already exists")
	ErrRegistrationDisabled = errors.New("registration is disabled")
	ErrSessionNotFound      = errors.New("session not found")
)

type User struct {
	Username          string     `json:"username"`
	PasswordHash      string     `json:"-"`
	Points            int        `json:"points"`
	CreatedAt         time.Time  `json:"createdAt"`
	PasswordChangedAt *time.Time `json:"passwordChangedAt"`
}

func (store *Store) RegistrationEnabled(ctx context.Context, override bool) (bool, error) {
	if override {
		return true, nil
	}
	var count int
	if err := store.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM users WHERE password_hash IS NOT NULL AND password_hash <> ''`).Scan(&count); err != nil {
		return false, err
	}
	return count == 0, nil
}

func (store *Store) RegisterUser(ctx context.Context, username, passwordHash string, allow bool, now time.Time) (*User, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var loginUsers int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM users WHERE password_hash IS NOT NULL AND password_hash <> ''`).Scan(&loginUsers); err != nil {
		return nil, err
	}
	if loginUsers > 0 && !allow {
		return nil, ErrRegistrationDisabled
	}

	var existingHash sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT password_hash FROM users WHERE username = ?`, username).Scan(&existingHash)
	switch {
	case err == nil && existingHash.Valid && existingHash.String != "":
		return nil, ErrUserExists
	case err == nil:
		if _, err := tx.ExecContext(ctx, `
			UPDATE users SET password_hash = ?, password_changed_at = ? WHERE username = ?`,
			passwordHash, formatTime(now), username); err != nil {
			return nil, err
		}
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO users(username, password_hash, points, created_at)
			VALUES (?, ?, 0, ?)`, username, passwordHash, formatTime(now)); err != nil {
			return nil, err
		}
	default:
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return store.User(ctx, username)
}

func (store *Store) User(ctx context.Context, username string) (*User, error) {
	var user User
	var passwordHash sql.NullString
	var createdAt string
	var changedAt sql.NullString
	err := store.db.QueryRowContext(ctx, `
		SELECT username, password_hash, points, created_at, password_changed_at
		FROM users WHERE username = ?`, username,
	).Scan(&user.Username, &passwordHash, &user.Points, &createdAt, &changedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUserNotFound
	}
	if err != nil {
		return nil, err
	}
	user.PasswordHash = passwordHash.String
	user.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return nil, err
	}
	user.PasswordChangedAt, err = parseNullableTime(changedAt)
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func (store *Store) UpdatePassword(ctx context.Context, username, passwordHash string, now time.Time) error {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
		UPDATE users SET password_hash = ?, password_changed_at = ? WHERE username = ?`,
		passwordHash, formatTime(now), username,
	)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return ErrUserNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE username = ?`, username); err != nil {
		return err
	}
	return tx.Commit()
}

func (store *Store) CreateSession(ctx context.Context, tokenHash, username string, createdAt, expiresAt time.Time) error {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, formatTime(createdAt)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO sessions(token_hash, username, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		tokenHash, username, formatTime(createdAt), formatTime(expiresAt),
	); err != nil {
		return err
	}
	return tx.Commit()
}

func (store *Store) SessionUser(ctx context.Context, tokenHash string, now, newExpiry time.Time) (*User, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var user User
	var passwordHash sql.NullString
	var createdAt string
	var changedAt sql.NullString
	err = tx.QueryRowContext(ctx, `
		SELECT u.username, u.password_hash, u.points, u.created_at, u.password_changed_at
		FROM sessions s JOIN users u ON u.username = s.username
		WHERE s.token_hash = ? AND s.expires_at > ?`, tokenHash, formatTime(now),
	).Scan(&user.Username, &passwordHash, &user.Points, &createdAt, &changedAt)
	if errors.Is(err, sql.ErrNoRows) {
		tx.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash)
		tx.Commit()
		return nil, ErrSessionNotFound
	}
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sessions SET expires_at = ? WHERE token_hash = ?`, formatTime(newExpiry), tokenHash); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	user.PasswordHash = passwordHash.String
	user.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return nil, err
	}
	user.PasswordChangedAt, err = parseNullableTime(changedAt)
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func (store *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := store.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash)
	return err
}

func (store *Store) DeleteSessionsForUser(ctx context.Context, username string) error {
	_, err := store.db.ExecContext(ctx, `DELETE FROM sessions WHERE username = ?`, username)
	return err
}
