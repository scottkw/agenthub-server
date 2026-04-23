package api

import (
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/scottkw/agenthub-server/internal/auth"
	"github.com/scottkw/agenthub-server/internal/blob"
	"github.com/scottkw/agenthub-server/internal/blobs"
	"github.com/scottkw/agenthub-server/internal/ids"
	"github.com/scottkw/agenthub-server/internal/realtime"
)

// BlobRoutes mounts /api/blobs/*.
//   POST /presign       — get upload URL
//   PUT  /upload/{id}   — in-process upload (file backend)
//   POST /{id}/commit   — verify upload, record metadata
//   GET  /{id}          — metadata + download URL
//   GET  /download/{id} — serve bytes (file backend)
func BlobRoutes(svc *auth.Service, store blob.Blob, pub realtime.Publisher) http.Handler {
	r := chi.NewRouter()
	r.Use(auth.RequireAuthOrTokenFromService(svc))
	r.Post("/presign", presignBlobHandler(svc, store))
	r.Post("/{id}/commit", commitBlobHandler(svc, store, pub))
	r.Get("/{id}", getBlobHandler(svc, store))

	// In-process upload/download are NOT behind the standard auth middleware
	// because the presign URL itself is the capability. However, for the file
	// backend we still require the JWT to be present — the client sends it.
	r.Put("/upload/{id}", uploadBlobHandler(svc, store))
	r.Get("/download/{id}", downloadBlobHandler(svc, store))
	return r
}

type presignBlobReq struct {
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
	SHA256      string `json:"sha256"`
}

func presignBlobHandler(svc *auth.Service, store blob.Blob) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in presignBlobReq
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid_json", err.Error())
			return
		}
		if in.ContentType == "" || in.SizeBytes <= 0 {
			WriteError(w, http.StatusBadRequest, "invalid_params", "content_type and positive size_bytes required")
			return
		}

		objectID := ids.New()
		putURL, err := store.PresignPut(r.Context(), objectID, in.ContentType, in.SizeBytes)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, "presign_failed", err.Error())
			return
		}

		WriteJSON(w, http.StatusOK, map[string]any{
			"put_url":    putURL,
			"object_id":  objectID,
		})
	}
}

func uploadBlobHandler(svc *auth.Service, store blob.Blob) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if err := store.Put(r.Context(), id, r.Body, r.Header.Get("Content-Type")); err != nil {
			WriteError(w, http.StatusInternalServerError, "upload_failed", err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func commitBlobHandler(svc *auth.Service, store blob.Blob, pub realtime.Publisher) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		accountID := auth.AccountID(r.Context())
		userID := auth.UserID(r.Context())

		exists, err := store.Exists(r.Context(), id)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, "check_failed", err.Error())
			return
		}
		if !exists {
			WriteError(w, http.StatusBadRequest, "upload_missing", "object has not been uploaded yet")
			return
		}

		obj := blobs.BlobObject{
			ID:              id,
			AccountID:       accountID,
			Key:             id,
			CreatedByUserID: userID,
		}
		// Decode optional metadata from the request body if present.
		var body struct {
			ContentType string `json:"content_type"`
			SizeBytes   int64  `json:"size_bytes"`
			SHA256      string `json:"sha256"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
			if body.ContentType != "" {
				obj.ContentType = body.ContentType
			}
			obj.SizeBytes = body.SizeBytes
			obj.SHA256 = body.SHA256
		}

		if err := blobs.Create(r.Context(), svc.DB(), obj); err != nil {
			WriteError(w, http.StatusInternalServerError, "commit_failed", err.Error())
			return
		}

		if pub != nil {
			pub.Publish(accountID, realtime.Event{
				Type: "blob.created",
				Data: map[string]any{
					"id":           obj.ID,
					"content_type": obj.ContentType,
					"size_bytes":   obj.SizeBytes,
					"sha256":       obj.SHA256,
				},
			})
		}

		dlURL, _ := store.PresignGet(r.Context(), id)
		WriteJSON(w, http.StatusOK, map[string]any{
			"id":           obj.ID,
			"account_id":   obj.AccountID,
			"key":          obj.Key,
			"content_type": obj.ContentType,
			"size_bytes":   obj.SizeBytes,
			"sha256":       obj.SHA256,
			"download_url": dlURL,
			"created_at":   time.Now().UTC(),
		})
	}
}

func getBlobHandler(svc *auth.Service, store blob.Blob) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		accountID := auth.AccountID(r.Context())

		obj, err := blobs.GetByID(r.Context(), svc.DB(), accountID, id)
		if err != nil {
			WriteError(w, http.StatusNotFound, "not_found", "blob not found")
			return
		}

		dlURL, _ := store.PresignGet(r.Context(), id)
		WriteJSON(w, http.StatusOK, map[string]any{
			"id":           obj.ID,
			"account_id":   obj.AccountID,
			"key":          obj.Key,
			"content_type": obj.ContentType,
			"size_bytes":   obj.SizeBytes,
			"sha256":       obj.SHA256,
			"download_url": dlURL,
			"created_at":   obj.CreatedAt,
		})
	}
}

func downloadBlobHandler(svc *auth.Service, store blob.Blob) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		accountID := auth.AccountID(r.Context())

		// Verify the object exists and belongs to the caller's account.
		obj, err := blobs.GetByID(r.Context(), svc.DB(), accountID, id)
		if err != nil {
			WriteError(w, http.StatusNotFound, "not_found", "blob not found")
			return
		}

		reader, err := store.Get(r.Context(), id)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, "read_failed", err.Error())
			return
		}
		defer reader.Close()

		if obj.ContentType != "" {
			w.Header().Set("Content-Type", obj.ContentType)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(w, reader)
	}
}
