package auth

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/scottkw/agenthub-server/internal/db/migrations"
	"github.com/scottkw/agenthub-server/internal/db/sqlite"
	"github.com/scottkw/agenthub-server/internal/ids"
	"github.com/scottkw/agenthub-server/internal/mail"
	"github.com/scottkw/agenthub-server/internal/tenancy"
	"github.com/stretchr/testify/require"
)

type capturingMailer struct {
	mu   sync.Mutex
	msgs []mail.Message
}

func (c *capturingMailer) Send(_ context.Context, m mail.Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.msgs = append(c.msgs, m)
	return nil
}

func newServiceStack(t *testing.T) (*Service, *sql.DB, *capturingMailer) {
	t.Helper()
	d, err := sqlite.Open(sqlite.Options{Path: filepath.Join(t.TempDir(), "t.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })
	require.NoError(t, migrations.Apply(context.Background(), d))

	key, err := LoadOrCreateJWTKey(context.Background(), d.SQL())
	require.NoError(t, err)

	mailer := &capturingMailer{}
	svc := NewService(Config{
		DB:              d.SQL(),
		Signer:          NewJWTSigner(key, "agenthub-server"),
		Mailer:          mailer,
		Log:             slog.Default(),
		TTL:             Lifetimes{Session: time.Hour, EmailVerify: 24 * time.Hour, PasswordReset: time.Hour},
		From:            "AgentHub <no-reply@test>",
		VerifyURLPrefix: "http://localhost/verify",
		ResetURLPrefix:  "http://localhost/reset",
	})
	return svc, d.SQL(), mailer
}

func TestSignup_CreatesUserAccountMembershipAndSendsVerifyEmail(t *testing.T) {
	svc, db, mailer := newServiceStack(t)

	out, err := svc.Signup(context.Background(), SignupInput{
		Email:       "Alice@Example.com",
		Password:    "correct-horse-battery-staple",
		AccountName: "Alice's Team",
	})
	require.NoError(t, err)
	require.NotEmpty(t, out.UserID)
	require.NotEmpty(t, out.AccountID)

	u, err := tenancy.GetUserByID(context.Background(), db, out.UserID)
	require.NoError(t, err)
	require.Equal(t, "alice@example.com", u.Email)
	require.True(t, strings.HasPrefix(u.PasswordHash, "$argon2id$"))

	_, err = tenancy.GetAccountByID(context.Background(), db, out.AccountID)
	require.NoError(t, err)

	m, err := tenancy.GetMembershipByAccountUser(context.Background(), db, out.AccountID, out.UserID)
	require.NoError(t, err)
	require.Equal(t, tenancy.RoleOwner, m.Role)

	require.Len(t, mailer.msgs, 1)
	require.Contains(t, mailer.msgs[0].Text, "http://localhost/verify?token=")
}

func TestSignup_DuplicateEmail(t *testing.T) {
	svc, _, _ := newServiceStack(t)
	_, err := svc.Signup(context.Background(), SignupInput{Email: "a@b.com", Password: "pwpwpwpw", AccountName: "x"})
	require.NoError(t, err)
	_, err = svc.Signup(context.Background(), SignupInput{Email: "A@B.COM", Password: "pwpwpwpw", AccountName: "y"})
	require.Error(t, err)
}

func TestVerifyEmail_MarksUserVerified(t *testing.T) {
	svc, db, mailer := newServiceStack(t)

	out, err := svc.Signup(context.Background(), SignupInput{Email: "a@b.com", Password: "pwpwpwpw", AccountName: "x"})
	require.NoError(t, err)

	body := mailer.msgs[0].Text
	idx := strings.Index(body, "token=")
	require.NotEqual(t, -1, idx)
	raw := strings.TrimSpace(body[idx+len("token="):])

	require.NoError(t, svc.VerifyEmail(context.Background(), raw))

	u, err := tenancy.GetUserByID(context.Background(), db, out.UserID)
	require.NoError(t, err)
	require.False(t, u.EmailVerifiedAt.IsZero())
}

func TestLogin_SuccessAndWrongPassword(t *testing.T) {
	svc, db, mailer := newServiceStack(t)

	_, err := svc.Signup(context.Background(), SignupInput{Email: "a@b.com", Password: "correctpassword", AccountName: "x"})
	require.NoError(t, err)

	_, err = svc.Login(context.Background(), LoginInput{Email: "a@b.com", Password: "correctpassword"})
	require.ErrorIs(t, err, ErrEmailNotVerified)

	body := mailer.msgs[0].Text
	raw := strings.TrimSpace(body[strings.Index(body, "token=")+len("token="):])
	require.NoError(t, svc.VerifyEmail(context.Background(), raw))

	tok, err := svc.Login(context.Background(), LoginInput{Email: "A@B.com", Password: "correctpassword"})
	require.NoError(t, err)
	require.NotEmpty(t, tok.Token)

	require.NoError(t, CheckSessionActive(context.Background(), db, tok.SessionID))

	_, err = svc.Login(context.Background(), LoginInput{Email: "a@b.com", Password: "wrong"})
	require.ErrorIs(t, err, ErrInvalidCredentials)
}

func TestLogout_RevokesSession(t *testing.T) {
	svc, db, mailer := newServiceStack(t)

	_, err := svc.Signup(context.Background(), SignupInput{Email: "a@b.com", Password: "pwpwpwpw", AccountName: "x"})
	require.NoError(t, err)
	body := mailer.msgs[0].Text
	raw := strings.TrimSpace(body[strings.Index(body, "token=")+len("token="):])
	require.NoError(t, svc.VerifyEmail(context.Background(), raw))

	tok, err := svc.Login(context.Background(), LoginInput{Email: "a@b.com", Password: "pwpwpwpw"})
	require.NoError(t, err)

	require.NoError(t, svc.Logout(context.Background(), tok.SessionID))
	err = CheckSessionActive(context.Background(), db, tok.SessionID)
	require.ErrorIs(t, err, ErrSessionRevoked)
}

func TestPasswordResetFlow(t *testing.T) {
	svc, _, mailer := newServiceStack(t)

	_, err := svc.Signup(context.Background(), SignupInput{Email: "a@b.com", Password: "oldpassword", AccountName: "x"})
	require.NoError(t, err)
	verifyBody := mailer.msgs[0].Text
	rawVerify := strings.TrimSpace(verifyBody[strings.Index(verifyBody, "token=")+len("token="):])
	require.NoError(t, svc.VerifyEmail(context.Background(), rawVerify))

	require.NoError(t, svc.RequestPasswordReset(context.Background(), "a@b.com"))
	require.Len(t, mailer.msgs, 2)
	resetBody := mailer.msgs[1].Text
	rawReset := strings.TrimSpace(resetBody[strings.Index(resetBody, "token=")+len("token="):])

	require.NoError(t, svc.ResetPassword(context.Background(), rawReset, "newpassword"))

	_, err = svc.Login(context.Background(), LoginInput{Email: "a@b.com", Password: "oldpassword"})
	require.ErrorIs(t, err, ErrInvalidCredentials)

	_, err = svc.Login(context.Background(), LoginInput{Email: "a@b.com", Password: "newpassword"})
	require.NoError(t, err)
}

func TestRequestPasswordReset_UnknownEmail_NoError(t *testing.T) {
	svc, _, mailer := newServiceStack(t)
	err := svc.RequestPasswordReset(context.Background(), "nobody@example.com")
	require.NoError(t, err)
	require.Len(t, mailer.msgs, 0)
}

// sentinel to avoid unused-import complaints
var _ = errors.New
var _ = ids.New
