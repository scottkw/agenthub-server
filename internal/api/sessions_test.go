package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/scottkw/agenthub-server/internal/auth"
	"github.com/scottkw/agenthub-server/internal/devices"
)

// routerWithSvc mounts /api/auth, /api/tokens, /api/devices, /api/sessions
// behind a single auth.Service (returned for the rare test that needs it).
// Reuses newRouterWithAuthInternal from auth_test.go.
func routerWithSvc(t *testing.T) (*chi.Mux, *stubMailer, *auth.Service) {
	t.Helper()
	r, mailer, svc := newRouterWithAuthInternal(t)
	r.Mount("/api/devices", DeviceRoutes(svc, devices.StubHeadscaler{}))
	r.Mount("/api/sessions", SessionRoutes(svc))
	return r, mailer, svc
}

func claimTestDevice(t *testing.T, r *chi.Mux, jwtBearer string) (deviceID, apiToken string) {
	t.Helper()
	authH := [2]string{"Authorization", "Bearer " + jwtBearer}

	rr := doJSON(t, r, "POST", "/api/devices/pair-code", nil, authH)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var pair struct {
		Code string `json:"code"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &pair))

	rr = doJSON(t, r, "POST", "/api/devices/claim", map[string]string{
		"code": pair.Code, "name": "laptop",
	})
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var claim struct {
		DeviceID string `json:"device_id"`
		APIToken string `json:"api_token"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &claim))
	return claim.DeviceID, claim.APIToken
}

func TestSessions_CreateListActivityEnd(t *testing.T) {
	r, mailer, _ := routerWithSvc(t)
	jwt := signUpAndLogin(t, r, mailer, "sess@example.com", "password9", "Sess")
	_, apiTok := claimTestDevice(t, r, jwt)

	tokH := [2]string{"Authorization", "Token " + apiTok}
	authH := [2]string{"Authorization", "Bearer " + jwt}

	// Create via device token.
	rr := doJSON(t, r, "POST", "/api/sessions", map[string]string{
		"label": "build", "cwd": "/home/u/proj",
	}, tokH)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var created struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &created))
	require.NotEmpty(t, created.ID)
	require.Equal(t, "running", created.Status)

	// List via bearer JWT.
	rr = doJSON(t, r, "GET", "/api/sessions", nil, authH)
	require.Equal(t, http.StatusOK, rr.Code)
	var list struct {
		Sessions []map[string]any `json:"sessions"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &list))
	require.Len(t, list.Sessions, 1)

	// Touch activity.
	rr = doJSON(t, r, "POST", "/api/sessions/"+created.ID+"/activity", nil, tokH)
	require.Equal(t, http.StatusNoContent, rr.Code, rr.Body.String())

	// End.
	rr = doJSON(t, r, "POST", "/api/sessions/"+created.ID+"/end", nil, tokH)
	require.Equal(t, http.StatusNoContent, rr.Code, rr.Body.String())

	// List — should now show stopped.
	rr = doJSON(t, r, "GET", "/api/sessions", nil, authH)
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &list))
	require.Equal(t, "stopped", list.Sessions[0]["status"])
}

func TestSessions_CreateRequiresDeviceToken(t *testing.T) {
	r, mailer, _ := routerWithSvc(t)
	jwt := signUpAndLogin(t, r, mailer, "nodev@example.com", "password9", "NoDev")
	authH := [2]string{"Authorization", "Bearer " + jwt}

	rr := doJSON(t, r, "POST", "/api/sessions", map[string]string{"label": "x"}, authH)
	require.Equal(t, http.StatusForbidden, rr.Code, rr.Body.String())
}
