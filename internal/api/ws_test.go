package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"

	"github.com/scottkw/agenthub-server/internal/realtime"
)

func dialTestWS(t *testing.T, serverURL, token string) *websocket.Conn {
	t.Helper()
	u, err := url.Parse(serverURL)
	require.NoError(t, err)
	u.Scheme = "ws"
	u.Path = "/ws"
	if token != "" {
		q := u.Query()
		q.Set("token", token)
		u.RawQuery = q.Encode()
	}
	c, _, err := websocket.Dial(context.Background(), u.String(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close(websocket.StatusNormalClosure, "cleanup") })
	return c
}

func TestWS_AuthedClientReceivesPublishedEvent(t *testing.T) {
	r, mailer, svc := newRouterWithAuthInternal(t)
	hub := realtime.NewInMemoryHub(realtime.HubConfig{
		HeartbeatInterval: time.Hour, StaleCullTimeout: time.Hour,
	})
	t.Cleanup(func() { _ = hub.Close() })

	r.Handle("/ws", WSRoutes(svc, hub))

	server := httptest.NewServer(r)
	t.Cleanup(server.Close)

	jwt := signUpAndLogin(t, r, mailer, "ws@example.com", "password9", "WS")

	var accountID string
	require.NoError(t, svc.DB().QueryRow(`SELECT id FROM accounts WHERE name='WS'`).Scan(&accountID))

	c := dialTestWS(t, server.URL, jwt)

	require.Eventually(t, func() bool { return hub.AccountConnCountForTest(accountID) == 1 }, 2*time.Second, 20*time.Millisecond)

	hub.Publish(accountID, realtime.Event{Type: "hello", Data: map[string]any{"x": 1}})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	typ, data, err := c.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, websocket.MessageText, typ)
	var e realtime.Event
	require.NoError(t, json.Unmarshal(data, &e))
	require.Equal(t, "hello", e.Type)
	require.Equal(t, float64(1), e.Data["x"])
}

func TestWS_MissingTokenIsRejected(t *testing.T) {
	r, _, svc := newRouterWithAuthInternal(t)
	hub := realtime.NewInMemoryHub(realtime.HubConfig{})
	t.Cleanup(func() { _ = hub.Close() })
	r.Handle("/ws", WSRoutes(svc, hub))

	server := httptest.NewServer(r)
	t.Cleanup(server.Close)

	u, err := url.Parse(server.URL)
	require.NoError(t, err)
	u.Scheme = "ws"
	u.Path = "/ws"

	_, resp, err := websocket.Dial(context.Background(), u.String(), nil)
	require.Error(t, err)
	require.NotNil(t, resp)
	require.Equal(t, 401, resp.StatusCode)
}
