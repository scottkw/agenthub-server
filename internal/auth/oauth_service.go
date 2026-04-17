package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/scottkw/agenthub-server/internal/ids"
	"github.com/scottkw/agenthub-server/internal/tenancy"
)

type OAuthServiceConfig struct {
	Provider    OAuthProvider
	OAuth2      *oauth2.Config
	UserInfoURL string
}

type OAuthService struct {
	svc  *Service
	cfg  OAuthServiceConfig
	http *http.Client
}

func NewOAuthService(svc *Service, cfg OAuthServiceConfig) *OAuthService {
	return &OAuthService{svc: svc, cfg: cfg, http: &http.Client{Timeout: 10 * time.Second}}
}

type FinishLoginInput struct {
	Code      string
	UserAgent string
	IP        string
}

type FinishLoginOutput struct {
	Token     string
	SessionID string
	UserID    string
	AccountID string
	Email     string
	Created   bool
}

type userinfo struct {
	ProviderUserID string
	Email          string
	Name           string
}

func (o *OAuthService) FinishLogin(ctx context.Context, in FinishLoginInput) (FinishLoginOutput, error) {
	tok, err := o.cfg.OAuth2.Exchange(ctx, in.Code)
	if err != nil {
		return FinishLoginOutput{}, fmt.Errorf("oauth exchange: %w", err)
	}

	ui, err := o.fetchUserInfo(ctx, tok.AccessToken)
	if err != nil {
		return FinishLoginOutput{}, err
	}
	if ui.Email == "" || ui.ProviderUserID == "" {
		return FinishLoginOutput{}, errors.New("oauth userinfo missing email or subject")
	}

	userID, accountID, created, err := o.upsertIdentity(ctx, ui)
	if err != nil {
		return FinishLoginOutput{}, err
	}

	sessionID := ids.New()
	if _, err := CreateSession(ctx, o.svc.cfg.DB, SessionInput{
		ID:        sessionID,
		UserID:    userID,
		AccountID: accountID,
		UserAgent: in.UserAgent,
		IP:        in.IP,
		TTL:       o.svc.cfg.TTL.Session,
	}); err != nil {
		return FinishLoginOutput{}, err
	}
	jwtStr, err := o.svc.cfg.Signer.Sign(Claims{
		SessionID: sessionID,
		UserID:    userID,
		AccountID: accountID,
		TTL:       o.svc.cfg.TTL.Session,
	})
	if err != nil {
		return FinishLoginOutput{}, err
	}

	return FinishLoginOutput{
		Token:     jwtStr,
		SessionID: sessionID,
		UserID:    userID,
		AccountID: accountID,
		Email:     ui.Email,
		Created:   created,
	}, nil
}

func (o *OAuthService) fetchUserInfo(ctx context.Context, accessToken string) (userinfo, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", o.cfg.UserInfoURL, nil)
	if err != nil {
		return userinfo{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := o.http.Do(req)
	if err != nil {
		return userinfo{}, fmt.Errorf("userinfo: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return userinfo{}, fmt.Errorf("userinfo: %d: %s", resp.StatusCode, string(b))
	}

	var raw map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return userinfo{}, fmt.Errorf("userinfo decode: %w", err)
	}

	switch o.cfg.Provider {
	case OAuthProviderGoogle:
		return parseGoogleUserInfo(raw), nil
	case OAuthProviderGitHub:
		return parseGitHubUserInfo(raw), nil
	default:
		return userinfo{}, fmt.Errorf("unsupported provider %q", o.cfg.Provider)
	}
}

func parseGoogleUserInfo(raw map[string]any) userinfo {
	return userinfo{
		ProviderUserID: asString(raw["sub"]),
		Email:          asString(raw["email"]),
		Name:           asString(raw["name"]),
	}
}

func parseGitHubUserInfo(raw map[string]any) userinfo {
	idStr := ""
	switch v := raw["id"].(type) {
	case float64:
		idStr = fmt.Sprintf("%d", int64(v))
	case string:
		idStr = v
	}
	return userinfo{
		ProviderUserID: idStr,
		Email:          asString(raw["email"]),
		Name:           asString(raw["name"]),
	}
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

func (o *OAuthService) upsertIdentity(ctx context.Context, ui userinfo) (userID, accountID string, created bool, _ error) {
	row := o.svc.cfg.DB.QueryRowContext(ctx, `
		SELECT user_id FROM oauth_identities WHERE provider = ? AND provider_user_id = ?`,
		string(o.cfg.Provider), ui.ProviderUserID)
	var existingUserID string
	err := row.Scan(&existingUserID)
	if err == nil {
		accRow := o.svc.cfg.DB.QueryRowContext(ctx, `
			SELECT account_id FROM memberships WHERE user_id = ? ORDER BY created_at LIMIT 1`, existingUserID)
		if err := accRow.Scan(&accountID); err != nil {
			return "", "", false, fmt.Errorf("upsertIdentity: resolve account: %w", err)
		}
		return existingUserID, accountID, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", "", false, fmt.Errorf("upsertIdentity lookup identity: %w", err)
	}

	var u tenancy.User
	u, err = tenancy.GetUserByEmail(ctx, o.svc.cfg.DB, ui.Email)
	if err == nil {
		_, err = o.svc.cfg.DB.ExecContext(ctx, `
			INSERT INTO oauth_identities (id, user_id, provider, provider_user_id, email)
			VALUES (?, ?, ?, ?, ?)`,
			ids.New(), u.ID, string(o.cfg.Provider), ui.ProviderUserID, strings.ToLower(ui.Email))
		if err != nil {
			return "", "", false, fmt.Errorf("link identity: %w", err)
		}
		accRow := o.svc.cfg.DB.QueryRowContext(ctx, `
			SELECT account_id FROM memberships WHERE user_id = ? ORDER BY created_at LIMIT 1`, u.ID)
		if err := accRow.Scan(&accountID); err != nil {
			return "", "", false, fmt.Errorf("upsertIdentity: resolve account for existing email: %w", err)
		}
		return u.ID, accountID, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", "", false, fmt.Errorf("upsertIdentity lookup user: %w", err)
	}

	newUserID := ids.New()
	newAccountID := ids.New()
	membershipID := ids.New()
	identityID := ids.New()
	slug := slugify(ui.Name, newAccountID)

	tx, err := o.svc.cfg.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", "", false, err
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx,
		`INSERT INTO users (id, email, name, email_verified_at) VALUES (?, ?, ?, datetime('now'))`,
		newUserID, strings.ToLower(ui.Email), ui.Name)
	if err != nil {
		return "", "", false, fmt.Errorf("create user: %w", err)
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO accounts (id, slug, name, plan) VALUES (?, ?, ?, 'self_hosted')`,
		newAccountID, slug, firstNonEmpty(ui.Name, "Personal"))
	if err != nil {
		return "", "", false, fmt.Errorf("create account: %w", err)
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO memberships (id, account_id, user_id, role) VALUES (?, ?, ?, 'owner')`,
		membershipID, newAccountID, newUserID)
	if err != nil {
		return "", "", false, fmt.Errorf("create membership: %w", err)
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO oauth_identities (id, user_id, provider, provider_user_id, email) VALUES (?, ?, ?, ?, ?)`,
		identityID, newUserID, string(o.cfg.Provider), ui.ProviderUserID, strings.ToLower(ui.Email))
	if err != nil {
		return "", "", false, fmt.Errorf("create identity: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", "", false, err
	}
	return newUserID, newAccountID, true, nil
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// DB returns the underlying *sql.DB — same semantics as Service.DB.
func (o *OAuthService) DB() *sql.DB { return o.svc.cfg.DB }
