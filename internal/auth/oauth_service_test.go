package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

func fakeOAuthServer(t *testing.T, userinfo map[string]any) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "fake-access",
			"token_type":   "Bearer",
			"expires_in":   3600,
		})
	})
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(userinfo)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func buildOAuthService(t *testing.T, fakeURL string) (*OAuthService, *Service) {
	t.Helper()
	svc, _, _ := newServiceStack(t)
	oauthCfg := &oauth2.Config{
		ClientID:     "c",
		ClientSecret: "s",
		RedirectURL:  "http://client/cb",
		Endpoint:     oauth2.Endpoint{TokenURL: fakeURL + "/token", AuthURL: fakeURL + "/auth"},
	}
	osvc := NewOAuthService(svc, OAuthServiceConfig{
		Provider:    OAuthProviderGoogle,
		OAuth2:      oauthCfg,
		UserInfoURL: fakeURL + "/userinfo",
	})
	return osvc, svc
}

func TestFinishLogin_CreatesUserAndSession_NewGoogleUser(t *testing.T) {
	fake := fakeOAuthServer(t, map[string]any{
		"sub":   "goog-123",
		"email": "new@example.com",
		"name":  "New User",
	})
	osvc, _ := buildOAuthService(t, fake.URL)

	out, err := osvc.FinishLogin(context.Background(), FinishLoginInput{
		Code:      "authcode",
		UserAgent: "ua",
		IP:        "ip",
	})
	require.NoError(t, err)
	require.NotEmpty(t, out.Token)
	require.NotEmpty(t, out.UserID)
	require.True(t, out.Created, "new user should be flagged Created=true")
	require.Equal(t, "new@example.com", out.Email)
}

func TestFinishLogin_ExistingIdentity_Reuses(t *testing.T) {
	fake := fakeOAuthServer(t, map[string]any{
		"sub":   "goog-777",
		"email": "existing@example.com",
		"name":  "X",
	})
	osvc, _ := buildOAuthService(t, fake.URL)

	first, err := osvc.FinishLogin(context.Background(), FinishLoginInput{Code: "c1"})
	require.NoError(t, err)
	require.True(t, first.Created)

	second, err := osvc.FinishLogin(context.Background(), FinishLoginInput{Code: "c2"})
	require.NoError(t, err)
	require.Equal(t, first.UserID, second.UserID, "same provider_user_id should resolve to same user")
	require.False(t, second.Created)
}
