// Package api contains HTTP handlers for the public JSON API.
package api

import (
	"context"
	"encoding/json"
	"net/http"
)

// Pinger is the subset of db.DB that health checks depend on.
type Pinger interface {
	Ping(ctx context.Context) error
}

// NewHealthHandler returns a /healthz handler that reports process + db status.
func NewHealthHandler(p Pinger, version string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]string{
			"status":  "ok",
			"version": version,
			"db":      "ok",
		}
		code := http.StatusOK

		if err := p.Ping(r.Context()); err != nil {
			resp["status"] = "degraded"
			resp["db"] = "down"
			code = http.StatusServiceUnavailable
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(resp)
	})
}
