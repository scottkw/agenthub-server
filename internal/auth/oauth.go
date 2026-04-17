package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/github"
	"golang.org/x/oauth2/google"
)

type OAuthProvider string

const (
	OAuthProviderGoogle OAuthProvider = "google"
	OAuthProviderGitHub OAuthProvider = "github"
)

type GoogleConfig struct {
	ClientID     string
	ClientSecret string
	RedirectURL  string
}

func (c GoogleConfig) OAuth2() *oauth2.Config {
	return &oauth2.Config{
		ClientID:     c.ClientID,
		ClientSecret: c.ClientSecret,
		RedirectURL:  c.RedirectURL,
		Scopes:       []string{"openid", "email", "profile"},
		Endpoint:     google.Endpoint,
	}
}

type GitHubConfig struct {
	ClientID     string
	ClientSecret string
	RedirectURL  string
}

func (c GitHubConfig) OAuth2() *oauth2.Config {
	return &oauth2.Config{
		ClientID:     c.ClientID,
		ClientSecret: c.ClientSecret,
		RedirectURL:  c.RedirectURL,
		Scopes:       []string{"user:email"},
		Endpoint:     github.Endpoint,
	}
}

type OAuthStateInput struct {
	Provider    OAuthProvider
	RedirectURI string
	TTL         time.Duration
}

type OAuthState struct {
	Provider    OAuthProvider
	RedirectURI string
}

func CreateOAuthState(ctx context.Context, db *sql.DB, in OAuthStateInput) (string, error) {
	if in.TTL <= 0 {
		return "", errors.New("CreateOAuthState: TTL must be > 0")
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	state := base64.RawURLEncoding.EncodeToString(raw)
	_, err := db.ExecContext(ctx, `
		INSERT INTO oauth_states (state, provider, redirect_uri, expires_at)
		VALUES (?, ?, ?, datetime('now', ?))`,
		state, string(in.Provider), in.RedirectURI,
		fmt.Sprintf("+%d seconds", int(in.TTL.Seconds())),
	)
	if err != nil {
		return "", fmt.Errorf("CreateOAuthState: %w", err)
	}
	return state, nil
}

func ConsumeOAuthState(ctx context.Context, db *sql.DB, raw string) (OAuthState, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return OAuthState{}, err
	}
	defer tx.Rollback()

	row := tx.QueryRowContext(ctx,
		`SELECT provider, redirect_uri, expires_at, consumed_at FROM oauth_states WHERE state = ?`,
		raw)
	var provider, redirectURI, expiresAt string
	var consumedAt sql.NullString
	err = row.Scan(&provider, &redirectURI, &expiresAt, &consumedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return OAuthState{}, ErrTokenNotFound
	}
	if err != nil {
		return OAuthState{}, fmt.Errorf("ConsumeOAuthState lookup: %w", err)
	}
	if consumedAt.Valid {
		return OAuthState{}, ErrTokenNotFound
	}

	t, err := time.Parse("2006-01-02 15:04:05", expiresAt)
	if err != nil {
		return OAuthState{}, fmt.Errorf("parse expires_at: %w", err)
	}
	if time.Now().After(t) {
		return OAuthState{}, ErrTokenExpired
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE oauth_states SET consumed_at = datetime('now') WHERE state = ?`, raw,
	); err != nil {
		return OAuthState{}, fmt.Errorf("mark consumed: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return OAuthState{}, fmt.Errorf("commit: %w", err)
	}
	return OAuthState{Provider: OAuthProvider(provider), RedirectURI: redirectURI}, nil
}
