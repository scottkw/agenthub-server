package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/scottkw/agenthub-server/internal/auth"
	"github.com/scottkw/agenthub-server/internal/db/migrations"
	"github.com/scottkw/agenthub-server/internal/db/sqlite"
	"github.com/scottkw/agenthub-server/internal/mail"
	"github.com/stretchr/testify/require"
)

type stubMailer struct {
	mu   sync.Mutex
	msgs []mail.Message
}

func (s *stubMailer) Send(_ context.Context, m mail.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.msgs = append(s.msgs, m)
	return nil
}

func newRouterWithAuth(t *testing.T) (*chi.Mux, *stubMailer) {
	t.Helper()
	d, err := sqlite.Open(sqlite.Options{Path: filepath.Join(t.TempDir(), "t.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })
	require.NoError(t, migrations.Apply(context.Background(), d))

	key, err := auth.LoadOrCreateJWTKey(context.Background(), d.SQL())
	require.NoError(t, err)

	mailer := &stubMailer{}
	svc := auth.NewService(auth.Config{
		DB:              d.SQL(),
		Signer:          auth.NewJWTSigner(key, "agenthub-server"),
		Mailer:          mailer,
		TTL:             auth.Lifetimes{Session: time.Hour, EmailVerify: time.Hour, PasswordReset: time.Hour},
		From:            "AgentHub <test@test>",
		VerifyURLPrefix: "http://t/verify",
		ResetURLPrefix:  "http://t/reset",
	})

	r := chi.NewRouter()
	r.Mount("/api/auth", AuthRoutes(svc))
	return r, mailer
}

func doJSON(t *testing.T, r http.Handler, method, path string, body any, headers ...[2]string) *httptest.ResponseRecorder {
	t.Helper()
	var bs []byte
	if body != nil {
		var err error
		bs, err = json.Marshal(body)
		require.NoError(t, err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(bs))
	req.Header.Set("Content-Type", "application/json")
	for _, h := range headers {
		req.Header.Set(h[0], h[1])
	}
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	return rr
}

func TestAuthRoutes_SignupVerifyLoginLogout(t *testing.T) {
	r, mailer := newRouterWithAuth(t)

	rr := doJSON(t, r, "POST", "/api/auth/signup", map[string]string{
		"email":        "a@b.com",
		"password":     "password1",
		"account_name": "Team",
	})
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	require.Len(t, mailer.msgs, 1)
	body := mailer.msgs[0].Text
	raw := strings.TrimSpace(body[strings.Index(body, "token=")+len("token="):])

	rr = doJSON(t, r, "POST", "/api/auth/verify", map[string]string{"token": raw})
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	rr = doJSON(t, r, "POST", "/api/auth/login", map[string]string{
		"email":    "a@b.com",
		"password": "password1",
	})
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	var loginResp struct{ Token string `json:"token"` }
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &loginResp))
	require.NotEmpty(t, loginResp.Token)

	rr = doJSON(t, r, "POST", "/api/auth/logout", nil,
		[2]string{"Authorization", "Bearer " + loginResp.Token})
	require.Equal(t, http.StatusNoContent, rr.Code, rr.Body.String())
}

func TestAuthRoutes_LoginWrongPassword(t *testing.T) {
	r, _ := newRouterWithAuth(t)
	_ = doJSON(t, r, "POST", "/api/auth/signup", map[string]string{
		"email": "a@b.com", "password": "password1", "account_name": "T",
	})
	rr := doJSON(t, r, "POST", "/api/auth/login", map[string]string{"email": "a@b.com", "password": "wrong"})
	require.Equal(t, http.StatusUnauthorized, rr.Code)
}
