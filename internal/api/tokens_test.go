package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAPITokens_CreateListRevoke(t *testing.T) {
	r, mailer := newRouterWithAuthAndTokens(t)

	_ = doJSON(t, r, "POST", "/api/auth/signup", map[string]string{
		"email":        "tok@example.com",
		"password":     "password9",
		"account_name": "Tok",
	})
	body := mailer.msgs[0].Text
	raw := strings.TrimSpace(body[strings.Index(body, "token=")+len("token="):])
	_ = doJSON(t, r, "POST", "/api/auth/verify", map[string]string{"token": raw})

	loginResp := doJSON(t, r, "POST", "/api/auth/login", map[string]string{
		"email": "tok@example.com", "password": "password9",
	})
	var login struct {
		Token string `json:"token"`
	}
	require.NoError(t, json.Unmarshal(loginResp.Body.Bytes(), &login))
	authH := [2]string{"Authorization", "Bearer " + login.Token}

	rr := doJSON(t, r, "POST", "/api/tokens", map[string]any{"name": "cli"}, authH)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var created struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &created))
	require.True(t, strings.HasPrefix(created.Token, "ahs_"))

	rr = doJSON(t, r, "GET", "/api/tokens", nil, authH)
	require.Equal(t, http.StatusOK, rr.Code)
	var list struct {
		Tokens []map[string]any `json:"tokens"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &list))
	require.Len(t, list.Tokens, 1)

	// Use the api token against a protected endpoint.
	rr = doJSON(t, r, "GET", "/api/tokens", nil, [2]string{"Authorization", "Token " + created.Token})
	require.Equal(t, http.StatusOK, rr.Code)

	rr = doJSON(t, r, "DELETE", "/api/tokens/"+created.ID, nil, authH)
	require.Equal(t, http.StatusNoContent, rr.Code)

	rr = doJSON(t, r, "GET", "/api/tokens", nil, [2]string{"Authorization", "Token " + created.Token})
	require.Equal(t, http.StatusUnauthorized, rr.Code)
}
