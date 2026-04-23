package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/scottkw/agenthub-server/internal/auth"
	"github.com/scottkw/agenthub-server/internal/tenancy"
)

func newRouterWithAdmin(t *testing.T) (*chi.Mux, *stubMailer, *auth.Service) {
	t.Helper()
	r, mailer, svc := newRouterWithAuthInternal(t)
	r.Mount("/api/admin", AdminRoutes(svc))
	return r, mailer, svc
}

func makeOperator(t *testing.T, svc *auth.Service, email string) {
	t.Helper()
	u, err := tenancy.GetUserByEmail(context.Background(), svc.DB(), email)
	require.NoError(t, err)
	_, err = svc.DB().ExecContext(context.Background(), `UPDATE users SET is_operator = 1 WHERE id = ?`, u.ID)
	require.NoError(t, err)
}

func TestAdmin_UsersForbiddenForNonOperator(t *testing.T) {
	r, mailer, _ := newRouterWithAdmin(t)
	jwt := signUpAndLogin(t, r, mailer, "norm@example.com", "password9", "Norm")
	authH := [2]string{"Authorization", "Bearer " + jwt}

	rr := doJSON(t, r, "GET", "/api/admin/users", nil, authH)
	require.Equal(t, http.StatusForbidden, rr.Code, rr.Body.String())
}

func TestAdmin_UsersListForOperator(t *testing.T) {
	r, mailer, svc := newRouterWithAdmin(t)
	jwt := signUpAndLogin(t, r, mailer, "op@example.com", "password9", "Op")
	makeOperator(t, svc, "op@example.com")
	authH := [2]string{"Authorization", "Bearer " + jwt}

	rr := doJSON(t, r, "GET", "/api/admin/users", nil, authH)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var body map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	require.NotNil(t, body["users"])
}

func TestAdmin_AccountsListForOperator(t *testing.T) {
	r, mailer, svc := newRouterWithAdmin(t)
	jwt := signUpAndLogin(t, r, mailer, "op2@example.com", "password9", "Op2")
	makeOperator(t, svc, "op2@example.com")
	authH := [2]string{"Authorization", "Bearer " + jwt}

	rr := doJSON(t, r, "GET", "/api/admin/accounts", nil, authH)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var body map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	require.NotNil(t, body["accounts"])
}

func TestAdmin_HealthForOperator(t *testing.T) {
	r, mailer, svc := newRouterWithAdmin(t)
	jwt := signUpAndLogin(t, r, mailer, "op3@example.com", "password9", "Op3")
	makeOperator(t, svc, "op3@example.com")
	authH := [2]string{"Authorization", "Bearer " + jwt}

	rr := doJSON(t, r, "GET", "/api/admin/health", nil, authH)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var body map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	require.NotNil(t, body["status"])
}
