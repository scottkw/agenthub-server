package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"

	"github.com/scottkw/agenthub-server/internal/auth"
	"github.com/scottkw/agenthub-server/internal/db/migrations"
	"github.com/scottkw/agenthub-server/internal/db/sqlite"
	"github.com/scottkw/agenthub-server/internal/mail"
)

func newRouterWithOAuth(t *testing.T, userinfo map[string]any) (*chi.Mux, *auth.Service, *httptest.Server) {
	t.Helper()
	d, err := sqlite.Open(sqlite.Options{Path: filepath.Join(t.TempDir(), "t.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })
	require.NoError(t, migrations.Apply(context.Background(), d))

	key, err := auth.LoadOrCreateJWTKey(context.Background(), d.SQL())
	require.NoError(t, err)

	svc := auth.NewService(auth.Config{
		DB:              d.SQL(),
		Signer:          auth.NewJWTSigner(key, "agenthub-server"),
		Mailer:          mail.NewNoop(nil),
		TTL:             auth.Lifetimes{Session: time.Hour, EmailVerify: time.Hour, PasswordReset: time.Hour},
		From:            "x",
		VerifyURLPrefix: "x",
		ResetURLPrefix:  "x",
	})

	var fake *httptest.Server
	if userinfo != nil {
		mux := http.NewServeMux()
		mux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"fake","token_type":"Bearer","expires_in":3600}`))
		})
		mux.HandleFunc("/userinfo", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(userinfo)
		})
		fake = httptest.NewServer(mux)
		t.Cleanup(fake.Close)
	}

	tokenURL := ""
	userinfoURL := ""
	if fake != nil {
		tokenURL = fake.URL + "/token"
		userinfoURL = fake.URL + "/userinfo"
	}
	googleProvider := OAuthProviderWiring{
		Provider: auth.OAuthProviderGoogle,
		OAuth2: &oauth2.Config{
			ClientID:     "clientid",
			ClientSecret: "secret",
			RedirectURL:  "http://t/api/auth/oauth/google/callback",
			Scopes:       []string{"email"},
			Endpoint: oauth2.Endpoint{
				AuthURL:  "https://accounts.google.com/o/oauth2/v2/auth",
				TokenURL: tokenURL,
			},
		},
		UserInfoURL: userinfoURL,
	}

	r := chi.NewRouter()
	r.Mount("/api/auth/oauth", OAuthRoutes(svc, []OAuthProviderWiring{googleProvider}))
	return r, svc, fake
}

func TestOAuth_StartRedirects(t *testing.T) {
	r, _, _ := newRouterWithOAuth(t, nil)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/auth/oauth/google/start", nil)
	r.ServeHTTP(rr, req)

	require.Equal(t, http.StatusFound, rr.Code)
	loc := rr.Header().Get("Location")
	require.True(t, strings.HasPrefix(loc, "https://accounts.google.com/"), "loc was %q", loc)
	require.Contains(t, loc, "state=")
	require.Contains(t, loc, "client_id=clientid")
}

func TestOAuth_CallbackCreatesUserAndReturnsToken(t *testing.T) {
	r, _, _ := newRouterWithOAuth(t, map[string]any{
		"sub":   "g-1",
		"email": "oauth-e2e@example.com",
		"name":  "OE2E",
	})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/auth/oauth/google/start", nil)
	r.ServeHTTP(rr, req)
	require.Equal(t, http.StatusFound, rr.Code)
	loc := rr.Header().Get("Location")
	state := extractParam(t, loc, "state")

	rr = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/auth/oauth/google/callback?code=authcode&state="+state, nil)
	r.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	var body map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	require.NotEmpty(t, body["token"])
	require.Equal(t, "oauth-e2e@example.com", body["email"])
	require.Equal(t, true, body["created"])
}

func TestOAuth_CallbackRejectsBadState(t *testing.T) {
	r, _, _ := newRouterWithOAuth(t, nil)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/auth/oauth/google/callback?code=x&state=bogus", nil)
	r.ServeHTTP(rr, req)
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func extractParam(t *testing.T, url, key string) string {
	t.Helper()
	idx := strings.Index(url, key+"=")
	require.NotEqual(t, -1, idx)
	rest := url[idx+len(key)+1:]
	if amp := strings.Index(rest, "&"); amp != -1 {
		rest = rest[:amp]
	}
	return rest
}
