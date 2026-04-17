package api

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/scottkw/agenthub-server/internal/auth"
	"github.com/scottkw/agenthub-server/internal/ids"
	"github.com/scottkw/agenthub-server/internal/sessions"
)

// SessionRoutes mounts /api/sessions/*. Create/activity/end require a
// device-scoped ahs_ token (caller IS the device); list accepts any auth.
func SessionRoutes(svc *auth.Service) http.Handler {
	r := chi.NewRouter()
	r.Use(auth.RequireAuthOrTokenFromService(svc))
	r.Get("/", listSessionsHandler(svc))
	r.Post("/", createSessionHandler(svc))
	r.Post("/{id}/activity", touchSessionHandler(svc))
	r.Post("/{id}/end", endSessionHandler(svc))
	return r
}

type createSessionReq struct {
	Label string `json:"label"`
	CWD   string `json:"cwd"`
}

func createSessionHandler(svc *auth.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		devID := auth.DeviceID(r.Context())
		if devID == "" {
			WriteError(w, http.StatusForbidden, "device_token_required",
				"creating a session requires a device-scoped ahs_ token")
			return
		}
		var in createSessionReq
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid_json", err.Error())
			return
		}
		id := ids.New()
		err := sessions.Create(r.Context(), svc.DB(), sessions.AgentSession{
			ID: id, AccountID: auth.AccountID(r.Context()),
			DeviceID: devID, Label: in.Label, CWD: in.CWD,
		})
		if err != nil {
			WriteError(w, http.StatusInternalServerError, "create_failed", err.Error())
			return
		}
		WriteJSON(w, http.StatusOK, map[string]any{
			"id":     id,
			"status": string(sessions.StatusRunning),
		})
	}
}

func listSessionsHandler(svc *auth.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		list, err := sessions.ListForAccount(r.Context(), svc.DB(), auth.AccountID(r.Context()))
		if err != nil {
			WriteError(w, http.StatusInternalServerError, "list_failed", err.Error())
			return
		}
		out := make([]map[string]any, 0, len(list))
		for _, s := range list {
			out = append(out, map[string]any{
				"id":               s.ID,
				"account_id":       s.AccountID,
				"device_id":        s.DeviceID,
				"label":            s.Label,
				"status":           string(s.Status),
				"cwd":              s.CWD,
				"started_at":       s.StartedAt,
				"ended_at":         s.EndedAt,
				"last_activity_at": s.LastActivityAt,
			})
		}
		WriteJSON(w, http.StatusOK, map[string]any{"sessions": out})
	}
}

func touchSessionHandler(svc *auth.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if auth.DeviceID(r.Context()) == "" {
			WriteError(w, http.StatusForbidden, "device_token_required",
				"activity reports require a device-scoped ahs_ token")
			return
		}
		id := chi.URLParam(r, "id")
		s, err := sessions.GetByID(r.Context(), svc.DB(), id)
		if err != nil || s.AccountID != auth.AccountID(r.Context()) || s.DeviceID != auth.DeviceID(r.Context()) {
			WriteError(w, http.StatusNotFound, "not_found", "session not found")
			return
		}
		if err := sessions.TouchActivity(r.Context(), svc.DB(), id); err != nil {
			WriteError(w, http.StatusInternalServerError, "activity_failed", err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func endSessionHandler(svc *auth.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if auth.DeviceID(r.Context()) == "" {
			WriteError(w, http.StatusForbidden, "device_token_required",
				"ending a session requires a device-scoped ahs_ token")
			return
		}
		id := chi.URLParam(r, "id")
		s, err := sessions.GetByID(r.Context(), svc.DB(), id)
		if err != nil || s.AccountID != auth.AccountID(r.Context()) || s.DeviceID != auth.DeviceID(r.Context()) {
			WriteError(w, http.StatusNotFound, "not_found", "session not found")
			return
		}
		if err := sessions.End(r.Context(), svc.DB(), id); err != nil {
			WriteError(w, http.StatusInternalServerError, "end_failed", err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
