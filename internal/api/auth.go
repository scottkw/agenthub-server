package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/scottkw/agenthub-server/internal/auth"
)

// AuthRoutes returns a chi router mounting the /api/auth/* endpoints.
func AuthRoutes(svc *auth.Service) http.Handler {
	r := chi.NewRouter()
	r.Post("/signup", signupHandler(svc))
	r.Post("/verify", verifyHandler(svc))
	r.Post("/login", loginHandler(svc))
	r.Post("/reset-request", resetRequestHandler(svc))
	r.Post("/reset", resetHandler(svc))
	r.Group(func(sub chi.Router) {
		sub.Use(auth.RequireAuthFromService(svc))
		sub.Post("/logout", logoutHandler(svc))
	})
	return r
}

type signupReq struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	AccountName string `json:"account_name"`
}

func signupHandler(svc *auth.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in signupReq
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid_json", err.Error())
			return
		}
		out, err := svc.Signup(r.Context(), auth.SignupInput{
			Email: in.Email, Password: in.Password, AccountName: in.AccountName,
		})
		if err != nil {
			WriteError(w, http.StatusBadRequest, "signup_failed", err.Error())
			return
		}
		WriteJSON(w, http.StatusOK, map[string]string{
			"user_id":    out.UserID,
			"account_id": out.AccountID,
		})
	}
}

type verifyReq struct {
	Token string `json:"token"`
}

func verifyHandler(svc *auth.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in verifyReq
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid_json", err.Error())
			return
		}
		if err := svc.VerifyEmail(r.Context(), in.Token); err != nil {
			switch {
			case errors.Is(err, auth.ErrTokenNotFound), errors.Is(err, auth.ErrTokenExpired):
				WriteError(w, http.StatusBadRequest, "verify_failed", "token invalid or expired")
			default:
				WriteError(w, http.StatusInternalServerError, "verify_failed", err.Error())
			}
			return
		}
		WriteJSON(w, http.StatusOK, map[string]string{"status": "verified"})
	}
}

type loginReq struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func loginHandler(svc *auth.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in loginReq
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid_json", err.Error())
			return
		}
		out, err := svc.Login(r.Context(), auth.LoginInput{
			Email: in.Email, Password: in.Password,
			UserAgent: r.UserAgent(), IP: r.RemoteAddr,
		})
		if err != nil {
			switch {
			case errors.Is(err, auth.ErrInvalidCredentials):
				WriteError(w, http.StatusUnauthorized, "invalid_credentials", "wrong email or password")
			case errors.Is(err, auth.ErrEmailNotVerified):
				WriteError(w, http.StatusForbidden, "email_not_verified", "please verify your email first")
			default:
				WriteError(w, http.StatusInternalServerError, "login_failed", err.Error())
			}
			return
		}
		WriteJSON(w, http.StatusOK, map[string]any{
			"token":      out.Token,
			"session_id": out.SessionID,
			"user_id":    out.UserID,
			"account_id": out.AccountID,
			"expires_at": out.ExpiresAt,
		})
	}
}

func logoutHandler(svc *auth.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sessionID := auth.SessionID(r.Context())
		if sessionID == "" {
			WriteError(w, http.StatusUnauthorized, "unauthorized", "no session")
			return
		}
		if err := svc.Logout(r.Context(), sessionID); err != nil {
			WriteError(w, http.StatusInternalServerError, "logout_failed", err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

type resetRequestReq struct {
	Email string `json:"email"`
}

func resetRequestHandler(svc *auth.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in resetRequestReq
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid_json", err.Error())
			return
		}
		if err := svc.RequestPasswordReset(r.Context(), in.Email); err != nil {
			WriteError(w, http.StatusInternalServerError, "reset_request_failed", err.Error())
			return
		}
		WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

type resetReq struct {
	Token    string `json:"token"`
	Password string `json:"password"`
}

func resetHandler(svc *auth.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in resetReq
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid_json", err.Error())
			return
		}
		if err := svc.ResetPassword(r.Context(), in.Token, in.Password); err != nil {
			switch {
			case errors.Is(err, auth.ErrTokenNotFound), errors.Is(err, auth.ErrTokenExpired):
				WriteError(w, http.StatusBadRequest, "reset_failed", "token invalid or expired")
			default:
				WriteError(w, http.StatusInternalServerError, "reset_failed", err.Error())
			}
			return
		}
		WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}
