package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"golang.org/x/oauth2"

	"github.com/scottkw/agenthub-server/internal/auth"
)

type OAuthProviderWiring struct {
	Provider    auth.OAuthProvider
	OAuth2      *oauth2.Config
	UserInfoURL string
}

func OAuthRoutes(svc *auth.Service, wirings []OAuthProviderWiring) http.Handler {
	r := chi.NewRouter()
	for _, w := range wirings {
		w := w
		osvc := auth.NewOAuthService(svc, auth.OAuthServiceConfig{
			Provider:    w.Provider,
			OAuth2:      w.OAuth2,
			UserInfoURL: w.UserInfoURL,
		})
		r.Get("/"+string(w.Provider)+"/start", oauthStartHandler(svc, w))
		r.Get("/"+string(w.Provider)+"/callback", oauthCallbackHandler(osvc))
	}
	return r
}

func oauthStartHandler(svc *auth.Service, w OAuthProviderWiring) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		state, err := auth.CreateOAuthState(r.Context(), svc.DB(), auth.OAuthStateInput{
			Provider:    w.Provider,
			RedirectURI: r.URL.Query().Get("redirect_uri"),
			TTL:         10 * time.Minute,
		})
		if err != nil {
			WriteError(rw, http.StatusInternalServerError, "oauth_start_failed", err.Error())
			return
		}
		http.Redirect(rw, r, w.OAuth2.AuthCodeURL(state), http.StatusFound)
	}
}

func oauthCallbackHandler(osvc *auth.OAuthService) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		state := r.URL.Query().Get("state")
		code := r.URL.Query().Get("code")
		if state == "" || code == "" {
			WriteError(rw, http.StatusBadRequest, "oauth_callback_bad_params", "missing code or state")
			return
		}
		_, err := auth.ConsumeOAuthState(r.Context(), osvc.DB(), state)
		if err != nil {
			switch {
			case errors.Is(err, auth.ErrTokenNotFound), errors.Is(err, auth.ErrTokenExpired):
				WriteError(rw, http.StatusBadRequest, "oauth_state_invalid", "state invalid or expired")
			default:
				WriteError(rw, http.StatusInternalServerError, "oauth_state_lookup_failed", err.Error())
			}
			return
		}

		out, err := osvc.FinishLogin(r.Context(), auth.FinishLoginInput{
			Code: code, UserAgent: r.UserAgent(), IP: r.RemoteAddr,
		})
		if err != nil {
			WriteError(rw, http.StatusInternalServerError, "oauth_finish_failed", err.Error())
			return
		}
		WriteJSON(rw, http.StatusOK, map[string]any{
			"token":      out.Token,
			"session_id": out.SessionID,
			"user_id":    out.UserID,
			"account_id": out.AccountID,
			"email":      out.Email,
			"created":    out.Created,
		})
	}
}
