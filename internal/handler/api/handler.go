package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/wj/smbis/internal/auth"
	"github.com/wj/smbis/internal/middleware"
	"github.com/wj/smbis/internal/model"
	"github.com/wj/smbis/internal/service/release"
	"github.com/wj/smbis/internal/service/sign"
	"github.com/wj/smbis/internal/store/sqlite"
)

// usernameRe is the allowed character set for usernames: 3–32 chars, [a-zA-Z0-9_-].
var usernameRe = regexp.MustCompile(`^[a-zA-Z0-9_-]{3,32}$`)

// Handler holds the dependencies for all JSON API endpoints.
type Handler struct {
	store      *sqlite.Store
	signSvc    *sign.Service
	releaseSvc *release.Service
}

// New constructs a Handler.
func New(store *sqlite.Store, signSvc *sign.Service, releaseSvc *release.Service) *Handler {
	return &Handler{
		store:      store,
		signSvc:    signSvc,
		releaseSvc: releaseSvc,
	}
}

// ---------------------------------------------------------------------------
// JSON helpers
// ---------------------------------------------------------------------------

// writeJSON sets Content-Type to application/json, writes the given HTTP status
// code, and marshals v into the response body. Encoding errors are logged.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("writeJSON encode", "error", err)
	}
}

// writeError writes a JSON object of the form {"error": msg} with the given
// HTTP status code.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// readJSON decodes the JSON request body into v.
func readJSON(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}

// ---------------------------------------------------------------------------
// Sign Request API
// ---------------------------------------------------------------------------

// CreateSignRequest handles POST /api/sign/requests.
//
// Body: {"name": "..."}
// Returns: {"request_id": "..."}
func (h *Handler) CreateSignRequest(w http.ResponseWriter, r *http.Request) {
	identity := middleware.MustGetIdentity(r.Context())

	var body struct {
		Name string `json:"name"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	req, err := h.signSvc.CreateRequest(r.Context(), body.Name, identity.UserID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	middleware.AuditLog(r, "sign.create_request", req.ID, "name", req.Name)
	writeJSON(w, http.StatusCreated, map[string]string{"request_id": req.ID})
}

// InitSignFileUpload handles POST /api/sign/requests/{id}/files/init.
//
// Body: {"file_name": "...", "file_size": 123}
// Returns: {"draft_id": "...", "chunk_size": 8388608}
func (h *Handler) InitSignFileUpload(w http.ResponseWriter, r *http.Request) {
	identity := middleware.MustGetIdentity(r.Context())
	requestID := chi.URLParam(r, "id")

	var body struct {
		FileName string `json:"file_name"`
		FileSize int64  `json:"file_size"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	resp, err := h.signSvc.InitFileUpload(r.Context(), requestID, identity.UserID, body.FileName, body.FileSize)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	middleware.AuditLog(r, "sign.init_file_upload", requestID,
		"draft_id", resp.DraftID,
		"file_name", body.FileName,
		"file_size", body.FileSize,
	)
	writeJSON(w, http.StatusOK, map[string]any{
		"draft_id":   resp.DraftID,
		"chunk_size": resp.ChunkSize,
	})
}

// UploadSignFilePart handles POST /api/sign/requests/{id}/files/{draftId}/part.
//
// Query param: part_number
// Body: raw binary chunk
// Returns: {"etag": "..."}
func (h *Handler) UploadSignFilePart(w http.ResponseWriter, r *http.Request) {
	draftID := chi.URLParam(r, "draftId")

	partNumberStr := r.URL.Query().Get("part_number")
	partNumber, err := strconv.Atoi(partNumberStr)
	if err != nil || partNumber < 1 {
		writeError(w, http.StatusBadRequest, "invalid part_number query param")
		return
	}

	size := r.ContentLength
	etag, err := h.signSvc.UploadPart(r.Context(), draftID, partNumber, r.Body, size)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"etag": etag})
}

// CompleteSignFileUpload handles POST /api/sign/requests/{id}/files/{draftId}/complete.
//
// Returns the created SignFile as JSON.
func (h *Handler) CompleteSignFileUpload(w http.ResponseWriter, r *http.Request) {
	identity := middleware.MustGetIdentity(r.Context())
	requestID := chi.URLParam(r, "id")
	draftID := chi.URLParam(r, "draftId")

	sf, err := h.signSvc.CompleteFileUpload(r.Context(), draftID, requestID, identity.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	middleware.AuditLog(r, "sign.complete_file_upload", requestID,
		"file_id", sf.ID,
		"file_name", sf.OriginalName,
	)
	writeJSON(w, http.StatusOK, sf)
}

// AbortSignFileUpload handles DELETE /api/sign/requests/{id}/files/{draftId}.
func (h *Handler) AbortSignFileUpload(w http.ResponseWriter, r *http.Request) {
	requestID := chi.URLParam(r, "id")
	draftID := chi.URLParam(r, "draftId")

	if err := h.signSvc.AbortFileUpload(r.Context(), draftID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	middleware.AuditLog(r, "sign.abort_file_upload", requestID, "draft_id", draftID)
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Admin Sign API
// ---------------------------------------------------------------------------

// AdminInitSignedUpload handles POST /api/admin/sign/files/{fileId}/signed-upload/init.
//
// Body: {"file_size": 123}
// Returns the UploadInitResponse as JSON.
func (h *Handler) AdminInitSignedUpload(w http.ResponseWriter, r *http.Request) {
	identity := middleware.MustGetIdentity(r.Context())
	fileID := chi.URLParam(r, "fileId")

	var body struct {
		FileSize int64 `json:"file_size"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	resp, err := h.signSvc.InitSignedUpload(r.Context(), fileID, identity.UserID, body.FileSize)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	middleware.AuditLog(r, "sign.admin_init_signed_upload", fileID,
		"draft_id", resp.DraftID,
		"file_size", body.FileSize,
	)
	writeJSON(w, http.StatusOK, map[string]any{
		"draft_id":   resp.DraftID,
		"chunk_size": resp.ChunkSize,
	})
}

// AdminUploadSignedPart handles POST /api/admin/sign/files/{fileId}/signed-upload/{draftId}/part.
//
// Query param: part_number
// Body: raw binary chunk
// Returns: {"etag": "..."}
func (h *Handler) AdminUploadSignedPart(w http.ResponseWriter, r *http.Request) {
	draftID := chi.URLParam(r, "draftId")

	partNumberStr := r.URL.Query().Get("part_number")
	partNumber, err := strconv.Atoi(partNumberStr)
	if err != nil || partNumber < 1 {
		writeError(w, http.StatusBadRequest, "invalid part_number query param")
		return
	}

	size := r.ContentLength
	etag, err := h.signSvc.UploadPart(r.Context(), draftID, partNumber, r.Body, size)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"etag": etag})
}

// AdminCompleteSignedUpload handles POST /api/admin/sign/files/{fileId}/signed-upload/{draftId}/complete.
func (h *Handler) AdminCompleteSignedUpload(w http.ResponseWriter, r *http.Request) {
	fileID := chi.URLParam(r, "fileId")
	draftID := chi.URLParam(r, "draftId")

	if err := h.signSvc.CompleteSignedUpload(r.Context(), draftID, fileID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	middleware.AuditLog(r, "sign.admin_complete_signed_upload", fileID, "draft_id", draftID)
	w.WriteHeader(http.StatusNoContent)
}

// AdminMarkSignFileFailed handles POST /api/admin/sign/files/{fileId}/fail.
//
// Body: {"reason": "..."}
func (h *Handler) AdminMarkSignFileFailed(w http.ResponseWriter, r *http.Request) {
	fileID := chi.URLParam(r, "fileId")

	var body struct {
		Reason string `json:"reason"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if err := h.signSvc.MarkFileFailed(r.Context(), fileID, body.Reason); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	middleware.AuditLog(r, "sign.admin_mark_file_failed", fileID, "reason", body.Reason)
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Release Request API
// ---------------------------------------------------------------------------

// CreateReleaseRequest handles POST /api/release/requests.
//
// Body: {"name": "...", "expected_url": "..."}
// Returns the created ReleaseRequest as JSON.
func (h *Handler) CreateReleaseRequest(w http.ResponseWriter, r *http.Request) {
	identity := middleware.MustGetIdentity(r.Context())

	var body struct {
		Name        string `json:"name"`
		ExpectedURL string `json:"expected_url"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	req, err := h.releaseSvc.CreateRequest(r.Context(), body.Name, identity.UserID, body.ExpectedURL)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	middleware.AuditLog(r, "release.create_request", req.ID,
		"name", req.Name,
		"expected_url", req.ExpectedURL,
	)
	writeJSON(w, http.StatusCreated, req)
}

// InitReleaseFileUpload handles POST /api/release/requests/{id}/files/init.
//
// Body: {"file_name": "...", "file_size": 123}
// Returns: {"draft_id": "...", "chunk_size": ...}
func (h *Handler) InitReleaseFileUpload(w http.ResponseWriter, r *http.Request) {
	identity := middleware.MustGetIdentity(r.Context())
	requestID := chi.URLParam(r, "id")

	var body struct {
		FileName string `json:"file_name"`
		FileSize int64  `json:"file_size"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	resp, err := h.releaseSvc.InitFileUpload(r.Context(), requestID, identity.UserID, body.FileName, body.FileSize)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	middleware.AuditLog(r, "release.init_file_upload", requestID,
		"draft_id", resp.DraftID,
		"file_name", body.FileName,
		"file_size", body.FileSize,
	)
	writeJSON(w, http.StatusOK, map[string]any{
		"draft_id":   resp.DraftID,
		"chunk_size": resp.ChunkSize,
	})
}

// UploadReleaseFilePart handles POST /api/release/requests/{id}/files/{draftId}/part.
//
// Query param: part_number
// Body: raw binary chunk
// Returns: {"etag": "..."}
func (h *Handler) UploadReleaseFilePart(w http.ResponseWriter, r *http.Request) {
	draftID := chi.URLParam(r, "draftId")

	partNumberStr := r.URL.Query().Get("part_number")
	partNumber, err := strconv.Atoi(partNumberStr)
	if err != nil || partNumber < 1 {
		writeError(w, http.StatusBadRequest, "invalid part_number query param")
		return
	}

	size := r.ContentLength
	etag, err := h.releaseSvc.UploadPart(r.Context(), draftID, partNumber, r.Body, size)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"etag": etag})
}

// CompleteReleaseFileUpload handles POST /api/release/requests/{id}/files/{draftId}/complete.
//
// Returns the created ReleaseFile as JSON.
func (h *Handler) CompleteReleaseFileUpload(w http.ResponseWriter, r *http.Request) {
	identity := middleware.MustGetIdentity(r.Context())
	requestID := chi.URLParam(r, "id")
	draftID := chi.URLParam(r, "draftId")

	rf, err := h.releaseSvc.CompleteFileUpload(r.Context(), draftID, requestID, identity.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	middleware.AuditLog(r, "release.complete_file_upload", requestID,
		"file_id", rf.ID,
		"file_name", rf.OriginalName,
	)
	writeJSON(w, http.StatusOK, rf)
}

// AbortReleaseFileUpload handles DELETE /api/release/requests/{id}/files/{draftId}.
func (h *Handler) AbortReleaseFileUpload(w http.ResponseWriter, r *http.Request) {
	requestID := chi.URLParam(r, "id")
	draftID := chi.URLParam(r, "draftId")

	if err := h.releaseSvc.AbortFileUpload(r.Context(), draftID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	middleware.AuditLog(r, "release.abort_file_upload", requestID, "draft_id", draftID)
	w.WriteHeader(http.StatusNoContent)
}

// AdminMarkReleaseDone handles POST /api/admin/release/requests/{id}/done.
func (h *Handler) AdminMarkReleaseDone(w http.ResponseWriter, r *http.Request) {
	requestID := chi.URLParam(r, "id")

	if err := h.releaseSvc.MarkDone(r.Context(), requestID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	middleware.AuditLog(r, "release.admin_mark_done", requestID)
	w.WriteHeader(http.StatusNoContent)
}

// VerifyReleaseURL handles POST /api/release/requests/{id}/verify.
//
// Returns the ReleaseVerification result as JSON.
func (h *Handler) VerifyReleaseURL(w http.ResponseWriter, r *http.Request) {
	requestID := chi.URLParam(r, "id")

	verification, err := h.releaseSvc.VerifyURL(r.Context(), requestID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	middleware.AuditLog(r, "release.verify_url", requestID,
		"reachable", verification.Reachable,
	)
	writeJSON(w, http.StatusOK, verification)
}

// ---------------------------------------------------------------------------
// User Management API
// ---------------------------------------------------------------------------

// CreateUser handles POST /api/admin/users.
//
// Body: {"username": "...", "password": "..."}
// Validates username is 3–32 chars, only [a-zA-Z0-9_-].
// Returns the created User as JSON (password field omitted).
func (h *Handler) CreateUser(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if !usernameRe.MatchString(body.Username) {
		writeError(w, http.StatusBadRequest, "username must be 3–32 characters and contain only [a-zA-Z0-9_-]")
		return
	}

	if body.Password == "" {
		writeError(w, http.StatusBadRequest, "password is required")
		return
	}

	now := time.Now().UTC()
	user := &model.User{
		ID:        auth.GenerateULID(),
		Username:  body.Username,
		Password:  body.Password,
		Disabled:  false,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := h.store.CreateUser(r.Context(), user); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	middleware.AuditLog(r, "admin.create_user", user.ID, "username", user.Username)

	// Return the user without exposing the password.
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":         user.ID,
		"username":   user.Username,
		"disabled":   user.Disabled,
		"created_at": user.CreatedAt,
		"updated_at": user.UpdatedAt,
	})
}

// ResetUserPassword handles POST /api/admin/users/{id}/reset-password.
//
// Body: {"password": "..."}
func (h *Handler) ResetUserPassword(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "id")

	var body struct {
		Password string `json:"password"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if body.Password == "" {
		writeError(w, http.StatusBadRequest, "password is required")
		return
	}

	if err := h.store.UpdateUserPassword(r.Context(), userID, body.Password); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	middleware.AuditLog(r, "admin.reset_user_password", userID)
	w.WriteHeader(http.StatusNoContent)
}

// DisableUser handles POST /api/admin/users/{id}/disable.
//
// Body: {"disabled": true} or {"disabled": false}
func (h *Handler) DisableUser(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "id")

	var body struct {
		Disabled *bool `json:"disabled"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Default to true (disable) when no body is provided, for backward compatibility.
	disabled := true
	if body.Disabled != nil {
		disabled = *body.Disabled
	}

	if err := h.store.SetUserDisabled(r.Context(), userID, disabled); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	action := "admin.disable_user"
	if !disabled {
		action = "admin.enable_user"
	}
	middleware.AuditLog(r, action, userID)
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// CLI Key API
// ---------------------------------------------------------------------------

// ListCLIKeys handles GET /api/admin/cli-keys.
//
// Returns all CLI keys as JSON (including revoked).
func (h *Handler) ListCLIKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := h.store.ListCLIKeys(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Ensure we never serialise a nil slice as JSON null.
	if keys == nil {
		keys = []model.CLIKey{}
	}

	writeJSON(w, http.StatusOK, keys)
}

// CreateCLIKey handles POST /api/admin/cli-keys.
//
// Body: {"name": "..."}
// Returns the created CLIKey as JSON (key_value is only visible on creation).
func (h *Handler) CreateCLIKey(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if body.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	keyValue, err := auth.GenerateCLIKey()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate CLI key")
		return
	}

	now := time.Now().UTC()
	key := &model.CLIKey{
		ID:        auth.GenerateULID(),
		Name:      body.Name,
		KeyValue:  keyValue,
		CreatedAt: now,
	}

	if err := h.store.CreateCLIKey(r.Context(), key); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	middleware.AuditLog(r, "admin.create_cli_key", key.ID, "name", key.Name)
	writeJSON(w, http.StatusCreated, key)
}

// RevokeCLIKey handles DELETE /api/admin/cli-keys/{id}.
func (h *Handler) RevokeCLIKey(w http.ResponseWriter, r *http.Request) {
	keyID := chi.URLParam(r, "id")

	if err := h.store.RevokeCLIKey(r.Context(), keyID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	middleware.AuditLog(r, "admin.revoke_cli_key", keyID)
	w.WriteHeader(http.StatusNoContent)
}

// Ensure the io and model packages are referenced (used in helpers above).
var _ = io.EOF
var _ = model.User{}
