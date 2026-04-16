package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/scottkw/agenthub-server/internal/ids"
	"github.com/scottkw/agenthub-server/internal/mail"
	"github.com/scottkw/agenthub-server/internal/tenancy"
)

type Lifetimes struct {
	Session       time.Duration
	EmailVerify   time.Duration
	PasswordReset time.Duration
}

type Config struct {
	DB              *sql.DB
	Signer          *JWTSigner
	Mailer          mail.Mailer
	Log             *slog.Logger
	TTL             Lifetimes
	From            string
	VerifyURLPrefix string
	ResetURLPrefix  string
}

type Service struct{ cfg Config }

func NewService(cfg Config) *Service {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	return &Service{cfg: cfg}
}

type SignupInput struct {
	Email       string
	Password    string
	AccountName string
}
type SignupOutput struct {
	UserID    string
	AccountID string
}

func (s *Service) Signup(ctx context.Context, in SignupInput) (SignupOutput, error) {
	if in.Email == "" || in.Password == "" {
		return SignupOutput{}, fmt.Errorf("Signup: email and password required")
	}
	hash, err := HashPassword(in.Password)
	if err != nil {
		return SignupOutput{}, err
	}

	userID := ids.New()
	accountID := ids.New()
	membershipID := ids.New()
	tokenID := ids.New()
	slug := slugify(in.AccountName, accountID)

	tx, err := s.cfg.DB.BeginTx(ctx, nil)
	if err != nil {
		return SignupOutput{}, err
	}
	defer tx.Rollback()

	if err := tenancyCreateUserTx(ctx, tx, tenancy.User{
		ID:           userID,
		Email:        in.Email,
		PasswordHash: hash,
	}); err != nil {
		return SignupOutput{}, err
	}
	if err := tenancyCreateAccountTx(ctx, tx, tenancy.Account{
		ID:   accountID,
		Slug: slug,
		Name: in.AccountName,
		Plan: "self_hosted",
	}); err != nil {
		return SignupOutput{}, err
	}
	if err := tenancyAddMembershipTx(ctx, tx, tenancy.Membership{
		ID:        membershipID,
		AccountID: accountID,
		UserID:    userID,
		Role:      tenancy.RoleOwner,
	}); err != nil {
		return SignupOutput{}, err
	}
	if err := tx.Commit(); err != nil {
		return SignupOutput{}, err
	}

	raw, err := CreateVerificationToken(ctx, s.cfg.DB, VerificationTokenInput{
		ID:      tokenID,
		Purpose: PurposeEmailVerify,
		UserID:  userID,
		Email:   strings.ToLower(in.Email),
		TTL:     s.cfg.TTL.EmailVerify,
	})
	if err != nil {
		return SignupOutput{}, err
	}

	verifyURL := s.cfg.VerifyURLPrefix + "?token=" + url.QueryEscape(raw)
	if err := s.cfg.Mailer.Send(ctx, mail.Message{
		To:      strings.ToLower(in.Email),
		Subject: "Verify your AgentHub email",
		Text:    "Click to verify your email: " + verifyURL,
	}); err != nil {
		s.cfg.Log.Warn("signup.mail.send_failed", "error", err)
	}

	return SignupOutput{UserID: userID, AccountID: accountID}, nil
}

func (s *Service) VerifyEmail(ctx context.Context, rawToken string) error {
	tok, err := ConsumeVerificationToken(ctx, s.cfg.DB, rawToken, PurposeEmailVerify)
	if err != nil {
		return err
	}
	return tenancy.MarkEmailVerified(ctx, s.cfg.DB, tok.UserID)
}

type LoginInput struct {
	Email     string
	Password  string
	UserAgent string
	IP        string
}
type LoginOutput struct {
	Token     string
	SessionID string
	ExpiresAt time.Time
	UserID    string
	AccountID string
}

func (s *Service) Login(ctx context.Context, in LoginInput) (LoginOutput, error) {
	u, err := tenancy.GetUserByEmail(ctx, s.cfg.DB, in.Email)
	if errors.Is(err, sql.ErrNoRows) {
		return LoginOutput{}, ErrInvalidCredentials
	}
	if err != nil {
		return LoginOutput{}, err
	}
	if u.PasswordHash == "" {
		return LoginOutput{}, ErrInvalidCredentials
	}
	ok, err := VerifyPassword(in.Password, u.PasswordHash)
	if err != nil || !ok {
		return LoginOutput{}, ErrInvalidCredentials
	}
	if u.EmailVerifiedAt.IsZero() {
		return LoginOutput{}, ErrEmailNotVerified
	}

	row := s.cfg.DB.QueryRowContext(ctx,
		`SELECT account_id FROM memberships WHERE user_id = ? ORDER BY created_at LIMIT 1`, u.ID)
	var accountID string
	if err := row.Scan(&accountID); err != nil {
		return LoginOutput{}, fmt.Errorf("Login: resolve account: %w", err)
	}

	sessionID := ids.New()
	if _, err := CreateSession(ctx, s.cfg.DB, SessionInput{
		ID:        sessionID,
		UserID:    u.ID,
		AccountID: accountID,
		UserAgent: in.UserAgent,
		IP:        in.IP,
		TTL:       s.cfg.TTL.Session,
	}); err != nil {
		return LoginOutput{}, err
	}

	tok, err := s.cfg.Signer.Sign(Claims{
		SessionID: sessionID,
		UserID:    u.ID,
		AccountID: accountID,
		TTL:       s.cfg.TTL.Session,
	})
	if err != nil {
		return LoginOutput{}, err
	}

	return LoginOutput{
		Token:     tok,
		SessionID: sessionID,
		ExpiresAt: time.Now().Add(s.cfg.TTL.Session),
		UserID:    u.ID,
		AccountID: accountID,
	}, nil
}

func (s *Service) Logout(ctx context.Context, sessionID string) error {
	return RevokeSession(ctx, s.cfg.DB, sessionID)
}

func (s *Service) RequestPasswordReset(ctx context.Context, email string) error {
	u, err := tenancy.GetUserByEmail(ctx, s.cfg.DB, email)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // avoid user-enumeration
	}
	if err != nil {
		return err
	}

	raw, err := CreateVerificationToken(ctx, s.cfg.DB, VerificationTokenInput{
		ID:      ids.New(),
		Purpose: PurposePasswordReset,
		UserID:  u.ID,
		Email:   u.Email,
		TTL:     s.cfg.TTL.PasswordReset,
	})
	if err != nil {
		return err
	}
	resetURL := s.cfg.ResetURLPrefix + "?token=" + url.QueryEscape(raw)
	if err := s.cfg.Mailer.Send(ctx, mail.Message{
		To:      u.Email,
		Subject: "Reset your AgentHub password",
		Text:    "Click to reset: " + resetURL,
	}); err != nil {
		s.cfg.Log.Warn("reset.mail.send_failed", "error", err)
	}
	return nil
}

func (s *Service) ResetPassword(ctx context.Context, rawToken, newPassword string) error {
	if newPassword == "" {
		return errors.New("ResetPassword: new password required")
	}
	tok, err := ConsumeVerificationToken(ctx, s.cfg.DB, rawToken, PurposePasswordReset)
	if err != nil {
		return err
	}
	hash, err := HashPassword(newPassword)
	if err != nil {
		return err
	}
	return tenancy.UpdatePasswordHash(ctx, s.cfg.DB, tok.UserID, hash)
}

// --- Tx helpers: mirror tenancy's Create* but take a *sql.Tx so Signup can
// commit atomically. Keeping them unexported here avoids a cross-package
// hack; duplication is small and localized. ---

func tenancyCreateUserTx(ctx context.Context, tx *sql.Tx, u tenancy.User) error {
	var hash any
	if u.PasswordHash == "" {
		hash = nil
	} else {
		hash = u.PasswordHash
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO users (id, email, password_hash, name, avatar_url) VALUES (?, ?, ?, ?, ?)`,
		u.ID, strings.ToLower(u.Email), hash, u.Name, u.AvatarURL,
	)
	if err != nil {
		return fmt.Errorf("createUserTx: %w", err)
	}
	return nil
}

func tenancyCreateAccountTx(ctx context.Context, tx *sql.Tx, a tenancy.Account) error {
	plan := a.Plan
	if plan == "" {
		plan = "self_hosted"
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO accounts (id, slug, name, plan) VALUES (?, ?, ?, ?)`,
		a.ID, a.Slug, a.Name, plan,
	)
	if err != nil {
		return fmt.Errorf("createAccountTx: %w", err)
	}
	return nil
}

func tenancyAddMembershipTx(ctx context.Context, tx *sql.Tx, m tenancy.Membership) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO memberships (id, account_id, user_id, role) VALUES (?, ?, ?, ?)`,
		m.ID, m.AccountID, m.UserID, string(m.Role),
	)
	if err != nil {
		return fmt.Errorf("addMembershipTx: %w", err)
	}
	return nil
}

// slugify makes a URL-safe account slug from the account name; falls back to
// the account id prefix if the name is empty or produces an empty slug.
func slugify(name, accountID string) string {
	var b strings.Builder
	prev := '-'
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prev = r
		case r == ' ' || r == '-' || r == '_':
			if prev != '-' {
				b.WriteRune('-')
				prev = '-'
			}
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" && len(accountID) >= 8 {
		slug = "acct-" + accountID[:8]
	}
	// Append a suffix of the account id to avoid slug collisions across users.
	if len(accountID) >= 6 {
		slug = slug + "-" + accountID[:6]
	}
	return slug
}
