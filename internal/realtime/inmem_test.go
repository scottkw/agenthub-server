package realtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

// wsServer spins up an httptest.Server whose handler accepts a WS upgrade
// and registers the connection with the given hub under the given account.
func wsServer(t *testing.T, hub *InMemoryHub, accountID string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		hub.Register(r.Context(), accountID, c)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func dialWS(t *testing.T, base string) *websocket.Conn {
	t.Helper()
	u, err := url.Parse(base)
	require.NoError(t, err)
	u.Scheme = "ws"
	c, _, err := websocket.Dial(context.Background(), u.String(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close(websocket.StatusNormalClosure, "test cleanup") })
	return c
}

func readEvent(t *testing.T, c *websocket.Conn, timeout time.Duration) Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	typ, data, err := c.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, websocket.MessageText, typ)
	var e Event
	require.NoError(t, json.Unmarshal(data, &e))
	return e
}

func TestInMemoryHub_PublishFansOutToAllConnections(t *testing.T) {
	hub := NewInMemoryHub(HubConfig{HeartbeatInterval: time.Hour, StaleCullTimeout: time.Hour})
	t.Cleanup(func() { _ = hub.Close() })

	srv := wsServer(t, hub, "acct1")
	c1 := dialWS(t, srv.URL)
	c2 := dialWS(t, srv.URL)

	require.Eventually(t, func() bool { return hub.accountConnCount("acct1") == 2 }, time.Second, 10*time.Millisecond)

	hub.Publish("acct1", Event{Type: "device.created", Data: map[string]any{"id": "x"}})

	e1 := readEvent(t, c1, 2*time.Second)
	e2 := readEvent(t, c2, 2*time.Second)
	require.Equal(t, "device.created", e1.Type)
	require.Equal(t, "device.created", e2.Type)
	require.Equal(t, "x", e1.Data["id"])
	require.Equal(t, "x", e2.Data["id"])
}

func TestInMemoryHub_EventsDoNotCrossAccounts(t *testing.T) {
	hub := NewInMemoryHub(HubConfig{HeartbeatInterval: time.Hour, StaleCullTimeout: time.Hour})
	t.Cleanup(func() { _ = hub.Close() })

	srv1 := wsServer(t, hub, "acctA")
	srv2 := wsServer(t, hub, "acctB")
	cA := dialWS(t, srv1.URL)
	cB := dialWS(t, srv2.URL)
	_ = cA
	_ = cB

	require.Eventually(t, func() bool {
		return hub.accountConnCount("acctA") == 1 && hub.accountConnCount("acctB") == 1
	}, time.Second, 10*time.Millisecond)

	hub.Publish("acctA", Event{Type: "x"})

	_ = readEvent(t, cA, 2*time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_, _, err := cB.Read(ctx)
	require.Error(t, err, "cB should not receive account-A's event")
}

func TestInMemoryHub_ConnectionRemovedOnClose(t *testing.T) {
	hub := NewInMemoryHub(HubConfig{HeartbeatInterval: time.Hour, StaleCullTimeout: time.Hour})
	t.Cleanup(func() { _ = hub.Close() })

	srv := wsServer(t, hub, "acct1")
	c := dialWS(t, srv.URL)
	require.Eventually(t, func() bool { return hub.accountConnCount("acct1") == 1 }, time.Second, 10*time.Millisecond)

	require.NoError(t, c.Close(websocket.StatusNormalClosure, "client done"))

	require.Eventually(t, func() bool { return hub.accountConnCount("acct1") == 0 }, 2*time.Second, 10*time.Millisecond)
}

func TestInMemoryHub_OverflowDropsEvents(t *testing.T) {
	hub := NewInMemoryHub(HubConfig{HeartbeatInterval: time.Hour, StaleCullTimeout: time.Hour, SendBuffer: 2})
	t.Cleanup(func() { _ = hub.Close() })

	srv := wsServer(t, hub, "acct1")
	c := dialWS(t, srv.URL)
	require.Eventually(t, func() bool { return hub.accountConnCount("acct1") == 1 }, time.Second, 10*time.Millisecond)

	for i := 0; i < 50; i++ {
		hub.Publish("acct1", Event{Type: "spam", Data: map[string]any{"i": i}})
	}

	got := 0
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		typ, data, err := c.Read(ctx)
		cancel()
		if err != nil {
			break
		}
		require.Equal(t, websocket.MessageText, typ)
		require.True(t, strings.Contains(string(data), `"spam"`))
		got++
		if got > 10 {
			break
		}
	}
	require.Greater(t, got, 0, "should have received at least one event before overflow")
}

// Silence unused imports if the compiler ever complains during TDD red phase.
var _ = sync.Mutex{}
