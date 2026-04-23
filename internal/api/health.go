package api

import (
	"context"
	"encoding/json"
	"net/http"
	"runtime"
	"time"
)

// Pinger is the subset of db.DB that health checks depend on.
type Pinger interface {
	Ping(ctx context.Context) error
}

// HealthChecker provides optional subsystem status checks.
type HealthChecker struct {
	Pinger    Pinger
	Version   string
	StartTime time.Time
}

// ServeHTTP writes a health check response. If the DB ping fails, status
// becomes "degraded" and the HTTP code is 503.
func (h *HealthChecker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	dbStatus := "ok"
	status := "ok"
	code := http.StatusOK

	if err := h.Pinger.Ping(r.Context()); err != nil {
		status = "degraded"
		dbStatus = "down"
		code = http.StatusServiceUnavailable
	}

	resp := map[string]any{
		"status":     status,
		"version":    h.Version,
		"uptime_sec": time.Since(h.StartTime).Seconds(),
		"db":         dbStatus,
		"go": map[string]any{
			"goroutines": runtime.NumGoroutine(),
			"memory_mb":  float64(mem.Alloc) / 1024 / 1024,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(resp)
}

// NewHealthHandler returns a /healthz handler. Use the returned
// *HealthChecker to set StartTime after the server boots.
func NewHealthHandler(p Pinger, version string) *HealthChecker {
	return &HealthChecker{Pinger: p, Version: version, StartTime: time.Now().UTC()}
}
