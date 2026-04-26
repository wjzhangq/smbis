package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/wj/smbis/internal/middleware"
	"github.com/wj/smbis/internal/service/sign"
)

// Handler handles CLI endpoints for automated signing workflows.
type Handler struct {
	signSvc     *sign.Service
	externalURL string
}

// New creates a new Handler with the provided sign service and external URL.
// externalURL is used to build upload_url values returned to CLI clients
// (e.g. "https://smb.example.com").
func New(signSvc *sign.Service, externalURL string) *Handler {
	return &Handler{
		signSvc:     signSvc,
		externalURL: externalURL,
	}
}

// ---------------------------------------------------------------------------
// JSON helpers
// ---------------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("cli handler: encode json response", "error", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// ---------------------------------------------------------------------------
// Response types
// ---------------------------------------------------------------------------

type fileResponse struct {
	FileID      string `json:"file_id"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Size        int64  `json:"size"`
	Status      string `json:"status"`
	DownloadURL string `json:"download_url,omitempty"`
	UploadURL   string `json:"upload_url"`
}

type listFilesResponse struct {
	RequestID string         `json:"request_id"`
	Name      string         `json:"name"`
	Status    string         `json:"status"`
	Files     []fileResponse `json:"files"`
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

// ListFiles handles GET /cli/sign/{requestId}/files.
// No auth required – the request ID acts as the token.
// Returns the sign request details and a list of pending files with presigned
// download URLs and server-side upload URLs.
func (h *Handler) ListFiles(w http.ResponseWriter, r *http.Request) {
	requestID := chi.URLParam(r, "requestId")

	req, files, err := h.signSvc.GetRequestWithFiles(r.Context(), requestID)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	fileResponses := make([]fileResponse, 0, len(files))
	for _, f := range files {
		var downloadURL string
		if f.Status == "pending" {
			url, _, urlErr := h.signSvc.GetFileDownloadURL(r.Context(), f.ID)
			if urlErr != nil {
				slog.Warn("cli handler: get file download url", "file_id", f.ID, "error", urlErr)
			} else {
				downloadURL = url
			}
		}

		uploadURL := fmt.Sprintf("%s/cli/sign/%s/files/%s/signed", h.externalURL, requestID, f.ID)

		fileResponses = append(fileResponses, fileResponse{
			FileID:      f.ID,
			Name:        f.OriginalName,
			Type:        f.FileType,
			Size:        f.SizeBytes,
			Status:      f.Status,
			DownloadURL: downloadURL,
			UploadURL:   uploadURL,
		})
	}

	writeJSON(w, http.StatusOK, listFilesResponse{
		RequestID: req.ID,
		Name:      req.Name,
		Status:    req.Status,
		Files:     fileResponses,
	})
}

// DownloadFile handles GET /cli/sign/{requestId}/files/{fileId}/download.
// No auth required. Streams the file directly from OSS, setting appropriate
// Content-Disposition, Content-Type, and Content-Length headers.
func (h *Handler) DownloadFile(w http.ResponseWriter, r *http.Request) {
	fileID := chi.URLParam(r, "fileId")

	rc, name, size, err := h.signSvc.GetFileReader(r.Context(), fileID)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	defer rc.Close()

	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.WriteHeader(http.StatusOK)

	if _, err := io.Copy(w, rc); err != nil {
		slog.Error("cli handler: stream file to client", "file_id", fileID, "error", err)
	}
}

// UploadSignedFile handles POST /cli/sign/{requestId}/files/{fileId}/signed.
// Requires CLI Key auth (enforced by middleware).
// Reads a multipart/form-data body with a field named "file" and uploads it
// directly to OSS via signSvc.UploadSignedFileDirect for small files.
func (h *Handler) UploadSignedFile(w http.ResponseWriter, r *http.Request) {
	requestID := chi.URLParam(r, "requestId")
	fileID := chi.URLParam(r, "fileId")

	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "failed to parse multipart form: "+err.Error())
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "missing form field \"file\": "+err.Error())
		return
	}
	defer file.Close()

	identity := middleware.MustGetIdentity(r.Context())

	if err := h.signSvc.UploadSignedFileDirect(r.Context(), fileID, identity.UserID, file, header.Size); err != nil {
		writeError(w, http.StatusInternalServerError, "upload failed: "+err.Error())
		return
	}

	middleware.AuditLog(r, "cli.upload_signed_file", fileID,
		"request_id", requestID,
		"file_name", header.Filename,
		"size", header.Size,
	)

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// InitUpload handles POST /cli/sign/{requestId}/files/{fileId}/upload/init.
// Requires CLI Key auth. Initialises a multipart upload session for the signed
// file and returns the draft ID and recommended chunk size.
func (h *Handler) InitUpload(w http.ResponseWriter, r *http.Request) {
	fileID := chi.URLParam(r, "fileId")

	var body struct {
		FileSize int64 `json:"file_size"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	identity := middleware.MustGetIdentity(r.Context())

	resp, err := h.signSvc.InitSignedUpload(r.Context(), fileID, identity.UserID, body.FileSize)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "init upload failed: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"draft_id":   resp.DraftID,
		"chunk_size": resp.ChunkSize,
	})
}

// UploadPart handles POST /cli/sign/{requestId}/files/{fileId}/upload/{draftId}/part.
// Requires CLI Key auth. Reads a raw binary chunk from the request body and
// forwards it to OSS as a numbered multipart part. The part number is taken
// from the "n" query parameter. Returns the ETag assigned by OSS.
func (h *Handler) UploadPart(w http.ResponseWriter, r *http.Request) {
	draftID := chi.URLParam(r, "draftId")

	nStr := r.URL.Query().Get("n")
	if nStr == "" {
		writeError(w, http.StatusBadRequest, "missing query parameter \"n\"")
		return
	}
	partNumber, err := strconv.Atoi(nStr)
	if err != nil || partNumber < 1 {
		writeError(w, http.StatusBadRequest, "invalid part number")
		return
	}

	size := r.ContentLength
	if size < 0 {
		size = 0
	}

	etag, err := h.signSvc.UploadPart(r.Context(), draftID, partNumber, r.Body, size)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "upload part failed: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"etag": etag})
}

// CompleteUpload handles POST /cli/sign/{requestId}/files/{fileId}/upload/{draftId}/complete.
// Requires CLI Key auth. Finalises the multipart upload, updates the sign file
// record, and transitions the parent request to done if all files are complete.
func (h *Handler) CompleteUpload(w http.ResponseWriter, r *http.Request) {
	fileID := chi.URLParam(r, "fileId")
	draftID := chi.URLParam(r, "draftId")

	if err := h.signSvc.CompleteSignedUpload(r.Context(), draftID, fileID); err != nil {
		writeError(w, http.StatusInternalServerError, "complete upload failed: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// MarkFileFailed handles POST /cli/sign/{requestId}/files/{fileId}/fail.
// Requires CLI Key auth. Marks the sign file as failed with the provided
// reason and transitions the parent request to done if no pending files remain.
func (h *Handler) MarkFileFailed(w http.ResponseWriter, r *http.Request) {
	requestID := chi.URLParam(r, "requestId")
	fileID := chi.URLParam(r, "fileId")

	var body struct {
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	if err := h.signSvc.MarkFileFailed(r.Context(), fileID, body.Reason); err != nil {
		writeError(w, http.StatusInternalServerError, "mark failed: "+err.Error())
		return
	}

	middleware.AuditLog(r, "cli.mark_file_failed", fileID,
		"request_id", requestID,
		"reason", body.Reason,
	)

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
