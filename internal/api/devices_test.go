package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/scottkw/agenthub-server/internal/devices"
	"github.com/scottkw/agenthub-server/internal/realtime"
)

// newRouterWithDevices builds a test router with /api/auth, /api/tokens, and
// /api/devices mounted (no rate-limit; tests inject headers directly).
func newRouterWithDevices(t *testing.T) (*chi.Mux, *stubMailer) {
	t.Helper()
	r, mailer, svc := newRouterWithAuthInternal(t)
	r.Mount("/api/devices", DeviceRoutes(svc, devices.StubHeadscaler{}, nil))
	return r, mailer
}

// signUpAndLogin returns a Bearer JWT suitable for Authorization headers.
func signUpAndLogin(t *testing.T, r *chi.Mux, mailer *stubMailer, email, pw, acct string) string {
	t.Helper()
	rr := doJSON(t, r, "POST", "/api/auth/signup", map[string]string{
		"email": email, "password": pw, "account_name": acct,
	})
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	// pluck verify token from mail body
	require.NotEmpty(t, mailer.msgs)
	body := mailer.msgs[len(mailer.msgs)-1].Text
	vTok := strings.TrimSpace(body[strings.Index(body, "token=")+len("token="):])
	_ = doJSON(t, r, "POST", "/api/auth/verify", map[string]string{"token": vTok})

	lr := doJSON(t, r, "POST", "/api/auth/login", map[string]string{"email": email, "password": pw})
	require.Equal(t, http.StatusOK, lr.Code, lr.Body.String())
	var login struct {
		Token string `json:"token"`
	}
	require.NoError(t, json.Unmarshal(lr.Body.Bytes(), &login))
	return login.Token
}

func TestDevices_PairAndClaim(t *testing.T) {
	r, mailer := newRouterWithDevices(t)
	jwt := signUpAndLogin(t, r, mailer, "dev@example.com", "password9", "Dev")
	authH := [2]string{"Authorization", "Bearer " + jwt}

	// Issue pair code.
	rr := doJSON(t, r, "POST", "/api/devices/pair-code", nil, authH)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var pair struct {
		Code      string    `json:"code"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &pair))
	require.NotEmpty(t, pair.Code)

	// Claim (no auth header).
	rr = doJSON(t, r, "POST", "/api/devices/claim", map[string]string{
		"code": pair.Code, "name": "laptop", "platform": "darwin", "app_version": "0.1.0",
	})
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var claim struct {
		DeviceID  string `json:"device_id"`
		APIToken  string `json:"api_token"`
		Tailscale struct {
			ControlURL  string `json:"control_url"`
			PreAuthKey  string `json:"pre_auth_key"`
			DERPMapJSON string `json:"derp_map_json"`
		} `json:"tailscale"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &claim))
	require.True(t, strings.HasPrefix(claim.APIToken, "ahs_"), "got %q", claim.APIToken)
	require.True(t, strings.HasPrefix(claim.Tailscale.PreAuthKey, "stub-"))

	// List devices (bearer auth).
	rr = doJSON(t, r, "GET", "/api/devices", nil, authH)
	require.Equal(t, http.StatusOK, rr.Code)
	var list struct {
		Devices []map[string]any `json:"devices"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &list))
	require.Len(t, list.Devices, 1)

	// Report tailscale-info (device-token auth).
	tokH := [2]string{"Authorization", "Token " + claim.APIToken}
	rr = doJSON(t, r, "POST", "/api/devices/"+claim.DeviceID+"/tailscale-info",
		map[string]string{"tailscale_node_id": "ts-node-xyz"}, tokH)
	require.Equal(t, http.StatusNoContent, rr.Code, rr.Body.String())

	// Fetch device, confirm it took.
	rr = doJSON(t, r, "GET", "/api/devices/"+claim.DeviceID, nil, authH)
	require.Equal(t, http.StatusOK, rr.Code)
	var got map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &got))
	require.Equal(t, "ts-node-xyz", got["tailscale_node_id"])
}

func TestDevices_ClaimRejectsUnknownCode(t *testing.T) {
	r, _ := newRouterWithDevices(t)
	rr := doJSON(t, r, "POST", "/api/devices/claim", map[string]string{
		"code": "NEVER", "name": "x",
	})
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
}

func TestDevices_TailscaleInfoRequiresDeviceToken(t *testing.T) {
	r, mailer := newRouterWithDevices(t)
	jwt := signUpAndLogin(t, r, mailer, "ts@example.com", "password9", "TS")
	authH := [2]string{"Authorization", "Bearer " + jwt}

	// Bearer JWT (no device) is rejected.
	rr := doJSON(t, r, "POST", "/api/devices/any-id/tailscale-info",
		map[string]string{"tailscale_node_id": "x"}, authH)
	require.Equal(t, http.StatusForbidden, rr.Code, rr.Body.String())
}

func TestDevices_SoftDelete(t *testing.T) {
	r, mailer := newRouterWithDevices(t)
	jwt := signUpAndLogin(t, r, mailer, "del@example.com", "password9", "Del")
	authH := [2]string{"Authorization", "Bearer " + jwt}

	// Pair + claim so there's a device to delete.
	rr := doJSON(t, r, "POST", "/api/devices/pair-code", nil, authH)
	var pair struct {
		Code string `json:"code"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &pair))
	rr = doJSON(t, r, "POST", "/api/devices/claim", map[string]string{
		"code": pair.Code, "name": "gone",
	})
	var claim struct {
		DeviceID string `json:"device_id"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &claim))

	rr = doJSON(t, r, "DELETE", "/api/devices/"+claim.DeviceID, nil, authH)
	require.Equal(t, http.StatusNoContent, rr.Code)

	// List should now be empty.
	rr = doJSON(t, r, "GET", "/api/devices", nil, authH)
	var list struct {
		Devices []map[string]any `json:"devices"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &list))
	require.Len(t, list.Devices, 0)
}

// capturingPublisher records every event; lives alongside the other
// test helpers in this file.
type capturingPublisher struct {
	mu     sync.Mutex
	events []capturedEvent
}

type capturedEvent struct {
	AccountID string
	Event     realtime.Event
}

func (c *capturingPublisher) Publish(accountID string, event realtime.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, capturedEvent{AccountID: accountID, Event: event})
}

func TestDevices_ClaimFiresDeviceCreatedEvent(t *testing.T) {
	r, mailer, svc := newRouterWithAuthInternal(t)
	pub := &capturingPublisher{}
	r.Mount("/api/devices", DeviceRoutes(svc, devices.StubHeadscaler{}, pub))

	jwt := signUpAndLogin(t, r, mailer, "evt@example.com", "password9", "Evt")
	authH := [2]string{"Authorization", "Bearer " + jwt}

	rr := doJSON(t, r, "POST", "/api/devices/pair-code", nil, authH)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var pair struct {
		Code string `json:"code"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &pair))

	rr = doJSON(t, r, "POST", "/api/devices/claim", map[string]string{
		"code": pair.Code, "name": "evt-laptop", "platform": "darwin",
	})
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	pub.mu.Lock()
	defer pub.mu.Unlock()
	require.Len(t, pub.events, 1, "expected exactly one published event")
	ev := pub.events[0]
	require.Equal(t, "device.created", ev.Event.Type)
	require.Equal(t, "evt-laptop", ev.Event.Data["name"])
	require.NotEmpty(t, ev.AccountID)
}
