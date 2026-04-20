package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/scottkw/agenthub-server/internal/auth"
	"github.com/scottkw/agenthub-server/internal/devices"
	"github.com/scottkw/agenthub-server/internal/realtime"
)

// DeviceRoutes mounts the /api/devices/* endpoints. The claim endpoint is
// NOT behind auth (the pair code authenticates); mount-time caller is
// expected to apply rate-limit middleware around the whole router.
func DeviceRoutes(svc *auth.Service, hs devices.Headscaler, pub realtime.Publisher) http.Handler {
	r := chi.NewRouter()

	// Public (code-authenticated): claim.
	r.Post("/claim", claimDeviceHandler(svc, hs, pub))

	// Bearer/Token authed: everything else.
	r.Group(func(sub chi.Router) {
		sub.Use(auth.RequireAuthOrTokenFromService(svc))
		sub.Post("/pair-code", issuePairCodeHandler(svc))
		sub.Get("/", listDevicesHandler(svc))
		sub.Get("/{id}", getDeviceHandler(svc))
		sub.Delete("/{id}", deleteDeviceHandler(svc))
		sub.Post("/{id}/tailscale-info", tailscaleInfoHandler(svc))
	})
	return r
}

func issuePairCodeHandler(svc *auth.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pc, err := devices.IssuePairCode(r.Context(), svc.DB(), devices.PairCodeInput{
			AccountID: auth.AccountID(r.Context()),
			UserID:    auth.UserID(r.Context()),
			TTL:       5 * time.Minute,
		})
		if err != nil {
			WriteError(w, http.StatusInternalServerError, "pair_code_failed", err.Error())
			return
		}
		WriteJSON(w, http.StatusOK, map[string]any{
			"code":       pc.Code,
			"expires_at": pc.ExpiresAt,
		})
	}
}

type claimDeviceReq struct {
	Code       string `json:"code"`
	Name       string `json:"name"`
	Platform   string `json:"platform"`
	AppVersion string `json:"app_version"`
}

func claimDeviceHandler(svc *auth.Service, hs devices.Headscaler, pub realtime.Publisher) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in claimDeviceReq
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid_json", err.Error())
			return
		}
		out, err := devices.ClaimDevice(r.Context(), svc.DB(), hs, devices.ClaimInput{
			Code: in.Code, Name: in.Name, Platform: in.Platform, AppVersion: in.AppVersion,
		})
		if err != nil {
			if errors.Is(err, devices.ErrPairCodeInvalid) {
				WriteError(w, http.StatusBadRequest, "invalid_code", "pair code invalid or expired")
				return
			}
			WriteError(w, http.StatusInternalServerError, "claim_failed", err.Error())
			return
		}
		if pub != nil {
			pub.Publish(out.Device.AccountID, realtime.Event{
				Type: "device.created",
				Data: map[string]any{
					"device_id":   out.Device.ID,
					"user_id":     out.Device.UserID,
					"name":        out.Device.Name,
					"platform":    out.Device.Platform,
					"app_version": out.Device.AppVersion,
				},
			})
		}
		WriteJSON(w, http.StatusOK, map[string]any{
			"device_id": out.Device.ID,
			"api_token": out.APIToken,
			"tailscale": map[string]any{
				"control_url":   out.PreAuthKey.ControlURL,
				"pre_auth_key":  out.PreAuthKey.Key,
				"derp_map_json": out.PreAuthKey.DERPMapJSON,
				"expires_at":    out.PreAuthKey.ExpiresAt,
			},
		})
	}
}

func listDevicesHandler(svc *auth.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		list, err := devices.ListDevicesForAccount(r.Context(), svc.DB(), auth.AccountID(r.Context()))
		if err != nil {
			WriteError(w, http.StatusInternalServerError, "list_failed", err.Error())
			return
		}
		out := make([]map[string]any, 0, len(list))
		for _, d := range list {
			out = append(out, deviceJSON(d))
		}
		WriteJSON(w, http.StatusOK, map[string]any{"devices": out})
	}
}

func getDeviceHandler(svc *auth.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		d, err := devices.GetDeviceByID(r.Context(), svc.DB(), id)
		if err != nil {
			WriteError(w, http.StatusNotFound, "not_found", "device not found")
			return
		}
		if d.AccountID != auth.AccountID(r.Context()) {
			WriteError(w, http.StatusNotFound, "not_found", "device not found")
			return
		}
		WriteJSON(w, http.StatusOK, deviceJSON(d))
	}
}

func deleteDeviceHandler(svc *auth.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		d, err := devices.GetDeviceByID(r.Context(), svc.DB(), id)
		if err != nil || d.AccountID != auth.AccountID(r.Context()) {
			WriteError(w, http.StatusNotFound, "not_found", "device not found")
			return
		}
		if err := devices.SoftDeleteDevice(r.Context(), svc.DB(), id); err != nil {
			WriteError(w, http.StatusInternalServerError, "delete_failed", err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

type tailscaleInfoReq struct {
	TailscaleNodeID string `json:"tailscale_node_id"`
}

func tailscaleInfoHandler(svc *auth.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Only a device-scoped token may report its own tailscale info.
		ctxDev := auth.DeviceID(r.Context())
		if ctxDev == "" {
			WriteError(w, http.StatusForbidden, "device_token_required",
				"this endpoint requires a device-scoped ahs_ token")
			return
		}
		id := chi.URLParam(r, "id")
		if id != ctxDev {
			WriteError(w, http.StatusForbidden, "wrong_device",
				"token is bound to a different device")
			return
		}
		var in tailscaleInfoReq
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid_json", err.Error())
			return
		}
		if err := devices.UpdateTailscaleInfo(r.Context(), svc.DB(), id, in.TailscaleNodeID); err != nil {
			WriteError(w, http.StatusInternalServerError, "update_failed", err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func deviceJSON(d devices.Device) map[string]any {
	return map[string]any{
		"id":                d.ID,
		"account_id":        d.AccountID,
		"user_id":           d.UserID,
		"name":              d.Name,
		"platform":          d.Platform,
		"app_version":       d.AppVersion,
		"tailscale_node_id": d.TailscaleNodeID,
		"last_seen_at":      d.LastSeenAt,
		"created_at":        d.CreatedAt,
	}
}
