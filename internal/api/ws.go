package api

import (
	"net/http"

	"github.com/coder/websocket"

	"github.com/scottkw/agenthub-server/internal/auth"
	"github.com/scottkw/agenthub-server/internal/realtime"
)

// WSRoutes returns an http.Handler for the /ws endpoint. Authenticates the
// request via JWT (header `Authorization: Bearer <jwt>` OR `?token=<jwt>`
// query param for browser clients that can't set custom WS headers),
// upgrades, and registers with the hub under the caller's account_id.
//
// Returned as http.Handler (not a chi router) so it can be mounted at an
// exact path without trailing-slash redirects that would break the upgrade
// handshake.
func WSRoutes(svc *auth.Service, hub *realtime.InMemoryHub) http.Handler {
	return wsHandler(svc, hub)
}

func wsHandler(svc *auth.Service, hub *realtime.InMemoryHub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, err := resolveWSAuth(svc, r)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		if err := auth.CheckSessionActive(r.Context(), svc.DB(), claims.SessionID); err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}

		hub.Register(r.Context(), claims.AccountID, c)
	}
}

// resolveWSAuth parses a JWT from the request. It accepts:
//  1. `Authorization: Bearer <jwt>` — for non-browser clients (daemon, CLI)
//  2. `?token=<jwt>` query param — for browser clients (the WebSocket
//     handshake has no general way to set custom headers).
//
// API tokens (`Authorization: Token ahs_...`) are NOT accepted on /ws in
// this plan.
func resolveWSAuth(svc *auth.Service, r *http.Request) (auth.Claims, error) {
	if raw := bearerFromHeader(r); raw != "" {
		return svc.Signer().Parse(raw)
	}
	if raw := r.URL.Query().Get("token"); raw != "" {
		return svc.Signer().Parse(raw)
	}
	return auth.Claims{}, errMissingToken
}

func bearerFromHeader(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const pfx = "Bearer "
	if len(h) > len(pfx) && h[:len(pfx)] == pfx {
		return h[len(pfx):]
	}
	return ""
}

var errMissingToken = &wsAuthError{"no token on Authorization header or ?token= query param"}

type wsAuthError struct{ msg string }

func (e *wsAuthError) Error() string { return e.msg }
