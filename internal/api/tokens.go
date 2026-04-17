package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/scottkw/agenthub-server/internal/auth"
	"github.com/scottkw/agenthub-server/internal/ids"
)

// APITokenRoutes mounts /api/tokens endpoints behind RequireAuthOrToken.
func APITokenRoutes(svc *auth.Service) http.Handler {
	r := chi.NewRouter()
	r.Use(auth.RequireAuthOrTokenFromService(svc))
	r.Post("/", createTokenHandler(svc))
	r.Get("/", listTokensHandler(svc))
	r.Delete("/{id}", revokeTokenHandler(svc))
	return r
}

type createTokenReq struct {
	Name string `json:"name"`
}

func createTokenHandler(svc *auth.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in createTokenReq
		_ = json.NewDecoder(r.Body).Decode(&in)
		raw, rec, err := auth.CreateAPIToken(r.Context(), svc.DB(), auth.APITokenInput{
			ID:        ids.New(),
			AccountID: auth.AccountID(r.Context()),
			UserID:    auth.UserID(r.Context()),
			Name:      strings.TrimSpace(in.Name),
		})
		if err != nil {
			WriteError(w, http.StatusInternalServerError, "create_failed", err.Error())
			return
		}
		WriteJSON(w, http.StatusOK, map[string]any{
			"id":    rec.ID,
			"token": raw,
			"name":  rec.Name,
		})
	}
}

func listTokensHandler(svc *auth.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		recs, err := auth.ListAPITokens(r.Context(), svc.DB(),
			auth.AccountID(r.Context()), auth.UserID(r.Context()))
		if err != nil {
			WriteError(w, http.StatusInternalServerError, "list_failed", err.Error())
			return
		}
		out := make([]map[string]any, 0, len(recs))
		for _, rec := range recs {
			out = append(out, map[string]any{
				"id":         rec.ID,
				"name":       rec.Name,
				"created_at": rec.CreatedAt,
			})
		}
		WriteJSON(w, http.StatusOK, map[string]any{"tokens": out})
	}
}

func revokeTokenHandler(svc *auth.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if id == "" {
			WriteError(w, http.StatusBadRequest, "missing_id", "path param id required")
			return
		}
		if err := auth.RevokeAPIToken(r.Context(), svc.DB(), id); err != nil {
			WriteError(w, http.StatusInternalServerError, "revoke_failed", err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
