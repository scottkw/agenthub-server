package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/scottkw/agenthub-server/internal/auth"
)

// AdminRoutes mounts /api/admin/* endpoints gated to operators.
func AdminRoutes(svc *auth.Service) http.Handler {
	r := chi.NewRouter()
	r.Use(auth.RequireAuthOrTokenFromService(svc))
	r.Use(auth.RequireOperator(svc))
	r.Get("/users", adminListUsersHandler(svc))
	r.Get("/accounts", adminListAccountsHandler(svc))
	r.Get("/health", adminHealthHandler(svc))
	return r
}

func adminListUsersHandler(svc *auth.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := svc.DB().QueryContext(r.Context(),
			`SELECT id, email, name, COALESCE(avatar_url, ''), is_operator, created_at FROM users WHERE deleted_at IS NULL ORDER BY created_at DESC`)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, "query_failed", err.Error())
			return
		}
		defer rows.Close()

		var out []map[string]any
		for rows.Next() {
			var id, email, name, avatar, createdAt string
			var isOp int
			if err := rows.Scan(&id, &email, &name, &avatar, &isOp, &createdAt); err != nil {
				continue
			}
			out = append(out, map[string]any{
				"id":          id,
				"email":       email,
				"name":        name,
				"avatar_url":  avatar,
				"is_operator": isOp == 1,
				"created_at":  createdAt,
			})
		}
		WriteJSON(w, http.StatusOK, map[string]any{"users": out})
	}
}

func adminListAccountsHandler(svc *auth.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := svc.DB().QueryContext(r.Context(),
			`SELECT id, slug, name, plan, created_at FROM accounts WHERE deleted_at IS NULL ORDER BY created_at DESC`)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, "query_failed", err.Error())
			return
		}
		defer rows.Close()

		var out []map[string]any
		for rows.Next() {
			var id, slug, name, plan, createdAt string
			if err := rows.Scan(&id, &slug, &name, &plan, &createdAt); err != nil {
				continue
			}
			out = append(out, map[string]any{
				"id":         id,
				"slug":       slug,
				"name":       name,
				"plan":       plan,
				"created_at": createdAt,
			})
		}
		WriteJSON(w, http.StatusOK, map[string]any{"accounts": out})
	}
}

func adminHealthHandler(svc *auth.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		WriteJSON(w, http.StatusOK, map[string]any{
			"status":    "ok",
			"timestamp": time.Now().UTC(),
		})
	}
}
