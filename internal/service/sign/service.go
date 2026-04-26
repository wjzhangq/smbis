package sign

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/wj/smbis/internal/auth"
	"github.com/wj/smbis/internal/model"
	ossstore "github.com/wj/smbis/internal/store/oss"
)

// allowedSignExts maps lower-cased file extensions to their file type classifier.
var allowedSignExts = map[string]string{
	".exe": "pe", ".dll": "pe", ".sys": "pe", ".ocx": "pe", ".msi": "pe", ".xml": "xml",
}

// UploadInitResponse is returned when a multipart upload is initialised.
type UploadInitResponse struct {
	DraftID   string `json:"draft_id"`
	ChunkSize int64  `json:"chunk_size"`
}

// Store is the persistence interface required by Service.
type Store interface {
	CreateSignRequest(ctx context.Context, r *model.SignRequest) (bool, error)
	GetSignRequest(ctx context.Context, id string) (*model.SignRequest, error)
	ListSignRequestsByUser(ctx context.Context, userID string, limit int) ([]model.SignRequest, error)
	ListAllSignRequests(ctx context.Context, limit int) ([]model.SignRequest, error)
	UpdateSignRequestStatus(ctx context.Context, id, status string) error
	CreateSignFile(ctx context.Context, f *model.SignFile) error
	GetSignFile(ctx context.Context, id string) (*model.SignFile, error)
	ListSignFilesByRequest(ctx context.Context, requestID string) ([]model.SignFile, error)
	UpdateSignFileSigned(ctx context.Context, id, signedOSSKey string, signedSize int64) error
	UpdateSignFileFailed(ctx context.Context, id, reason string) error
	CountSignFileStatuses(ctx context.Context, requestID string) (total, pending int, err error)
	CreateUploadDraft(ctx context.Context, d *model.UploadDraft) error
	GetUploadDraft(ctx context.Context, id string) (*model.UploadDraft, error)
	UpdateUploadDraftParts(ctx context.Context, id, partsJSON string) error
	AppendUploadDraftPart(ctx context.Context, id string, partNumber int, etag string) error
	DeleteUploadDraft(ctx context.Context, id string) error
}

// OSSClient is the object-storage interface required by Service.
type OSSClient interface {
	FullKey(key string) string
	InitMultipartUpload(ctx context.Context, ossKey string) (string, error)
	UploadPart(ctx context.Context, ossKey, uploadID string, partNumber int, reader io.Reader, size int64) (string, error)
	CompleteMultipartUpload(ctx context.Context, ossKey, uploadID string, parts []ossstore.UploadPart) error
	AbortMultipartUpload(ctx context.Context, ossKey, uploadID string) error
	DeleteObject(ctx context.Context, ossKey string) error
	GetPresignedURL(ctx context.Context, ossKey string) (string, error)
	GetObject(ctx context.Context, ossKey string) (io.ReadCloser, error)
	PutObject(ctx context.Context, ossKey string, reader io.Reader, size int64) error
}

// Service implements the signing-request business logic.
type Service struct {
	store       Store
	oss         OSSClient
	chunkSize   int64
	maxFileSize int64
}

// New creates a Service with the provided store, OSS client, and upload limits.
func New(store Store, oss OSSClient, chunkSize int64, maxFileSize int64) *Service {
	return &Service{
		store:       store,
		oss:         oss,
		chunkSize:   chunkSize,
		maxFileSize: maxFileSize,
	}
}

// ---------------------------------------------------------------------------
// Sign Request
// ---------------------------------------------------------------------------

// CreateRequest creates a new sign request owned by userID.
// name must be between 1 and 100 characters. The ID is an 8-character hex
// string; up to 5 attempts are made in case of a collision.
func (s *Service) CreateRequest(ctx context.Context, name, userID string) (*model.SignRequest, error) {
	name = strings.TrimSpace(name)
	if len(name) < 1 || len(name) > 100 {
		return nil, errors.New("sign: name must be between 1 and 100 characters")
	}

	now := time.Now().UTC()
	req := &model.SignRequest{
		Name:      name,
		UserID:    userID,
		Status:    model.SignRequestPending,
		CreatedAt: now,
		UpdatedAt: now,
	}

	const maxAttempts = 5
	for attempt := 0; attempt < maxAttempts; attempt++ {
		req.ID = auth.GenerateRequestID()
		inserted, err := s.store.CreateSignRequest(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("sign: create request: %w", err)
		}
		if inserted {
			return req, nil
		}
	}

	return nil, errors.New("sign: failed to generate a unique request ID after 5 attempts")
}

// ---------------------------------------------------------------------------
// Source file upload (multipart)
// ---------------------------------------------------------------------------

// InitFileUpload starts a multipart upload for a source file attached to a
// sign request. The file extension must be one of the allowed signing types and
// the file size must not exceed maxFileSize. Returns the draft ID and the
// recommended chunk size the caller should use.
func (s *Service) InitFileUpload(ctx context.Context, requestID, userID, fileName string, fileSize int64) (*UploadInitResponse, error) {
	ext := strings.ToLower(filepath.Ext(fileName))
	fileType, ok := allowedSignExts[ext]
	if !ok {
		return nil, fmt.Errorf("sign: unsupported file extension %q; allowed: .exe .dll .sys .ocx .msi .xml", ext)
	}

	if fileSize > s.maxFileSize {
		return nil, fmt.Errorf("sign: file size %d exceeds maximum allowed size %d", fileSize, s.maxFileSize)
	}

	fileID := auth.GenerateULID()
	ossKey := s.oss.FullKey(ossstore.SignSourceKey(requestID, fileID, fileName))

	uploadID, err := s.oss.InitMultipartUpload(ctx, ossKey)
	if err != nil {
		return nil, fmt.Errorf("sign: init multipart upload: %w", err)
	}

	now := time.Now().UTC()
	draft := &model.UploadDraft{
		ID:            fileID,
		CreatedBy:     userID,
		OSSUploadID:   uploadID,
		OSSKey:        ossKey,
		TotalSize:     fileSize,
		UploadedParts: "[]",
		ExpiresAt:     now.Add(7 * 24 * time.Hour),
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	if err := s.store.CreateUploadDraft(ctx, draft); err != nil {
		// Best-effort abort of the OSS upload so we do not leak in-progress uploads.
		_ = s.oss.AbortMultipartUpload(ctx, ossKey, uploadID)
		return nil, fmt.Errorf("sign: create upload draft: %w", err)
	}

	// Silence the unused import warning; fileType is used when completing.
	_ = fileType

	return &UploadInitResponse{
		DraftID:   fileID,
		ChunkSize: s.chunkSize,
	}, nil
}

// UploadPart forwards a single part of a multipart upload to OSS and atomically
// appends the resulting ETag in the draft's parts list.
func (s *Service) UploadPart(ctx context.Context, draftID string, partNumber int, reader io.Reader, size int64) (string, error) {
	draft, err := s.store.GetUploadDraft(ctx, draftID)
	if err != nil {
		return "", fmt.Errorf("sign: get upload draft: %w", err)
	}
	if draft == nil {
		return "", fmt.Errorf("sign: upload draft %q not found", draftID)
	}

	etag, err := s.oss.UploadPart(ctx, draft.OSSKey, draft.OSSUploadID, partNumber, reader, size)
	if err != nil {
		return "", fmt.Errorf("sign: upload part %d: %w", partNumber, err)
	}

	// Atomically append the part to the draft's parts list.
	if err := s.store.AppendUploadDraftPart(ctx, draftID, partNumber, etag); err != nil {
		return "", fmt.Errorf("sign: append upload draft part: %w", err)
	}

	return etag, nil
}

// CompleteFileUpload finalises a multipart upload, creates the sign_file record
// in the database, and deletes the draft. The returned SignFile represents the
// newly persisted file. The requestID is validated against the draft's OSS key
// to prevent cross-request file injection.
func (s *Service) CompleteFileUpload(ctx context.Context, draftID, requestID, userID string) (*model.SignFile, error) {
	draft, err := s.store.GetUploadDraft(ctx, draftID)
	if err != nil {
		return nil, fmt.Errorf("sign: get upload draft: %w", err)
	}
	if draft == nil {
		return nil, fmt.Errorf("sign: upload draft %q not found", draftID)
	}

	// Verify the draft belongs to this request by checking the OSS key pattern.
	expectedPrefix := fmt.Sprintf("sign/%s/source/", requestID)
	if !strings.Contains(draft.OSSKey, expectedPrefix) {
		return nil, fmt.Errorf("sign: draft %q does not belong to request %q", draftID, requestID)
	}

	var parts []ossstore.UploadPart
	if draft.UploadedParts != "" && draft.UploadedParts != "[]" {
		if err := json.Unmarshal([]byte(draft.UploadedParts), &parts); err != nil {
			return nil, fmt.Errorf("sign: parse uploaded parts: %w", err)
		}
	}

	if err := s.oss.CompleteMultipartUpload(ctx, draft.OSSKey, draft.OSSUploadID, parts); err != nil {
		return nil, fmt.Errorf("sign: complete multipart upload: %w", err)
	}

	// Derive the original file name from the OSS key:
	// key format is "<prefix>/sign/<requestID>/source/<fileID>-<originalName>"
	originalName := extractOriginalName(draft.OSSKey)

	ext := strings.ToLower(filepath.Ext(originalName))
	fileType, ok := allowedSignExts[ext]
	if !ok {
		fileType = "pe" // fallback; extension was validated at init time
	}

	now := time.Now().UTC()
	sf := &model.SignFile{
		ID:           draftID,
		RequestID:    requestID,
		OriginalName: originalName,
		FileType:     fileType,
		SizeBytes:    draft.TotalSize,
		SourceOSSKey: draft.OSSKey,
		Status:       model.SignFileStatusPending,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	if err := s.store.CreateSignFile(ctx, sf); err != nil {
		return nil, fmt.Errorf("sign: create sign file: %w", err)
	}

	if err := s.store.DeleteUploadDraft(ctx, draftID); err != nil {
		// Non-fatal: the file has been created; log-worthy but not blocking.
		_ = err
	}

	return sf, nil
}

// AbortFileUpload cancels an in-progress multipart upload and removes the
// draft record.
func (s *Service) AbortFileUpload(ctx context.Context, draftID string) error {
	draft, err := s.store.GetUploadDraft(ctx, draftID)
	if err != nil {
		return fmt.Errorf("sign: get upload draft: %w", err)
	}
	if draft == nil {
		return fmt.Errorf("sign: upload draft %q not found", draftID)
	}

	if err := s.oss.AbortMultipartUpload(ctx, draft.OSSKey, draft.OSSUploadID); err != nil {
		return fmt.Errorf("sign: abort multipart upload: %w", err)
	}

	if err := s.store.DeleteUploadDraft(ctx, draftID); err != nil {
		return fmt.Errorf("sign: delete upload draft: %w", err)
	}

	return nil
}

// ---------------------------------------------------------------------------
// Query helpers
// ---------------------------------------------------------------------------

// GetRequestWithFiles returns a sign request together with all its associated
// files.
func (s *Service) GetRequestWithFiles(ctx context.Context, id string) (*model.SignRequest, []model.SignFile, error) {
	req, err := s.store.GetSignRequest(ctx, id)
	if err != nil {
		return nil, nil, fmt.Errorf("sign: get sign request: %w", err)
	}
	if req == nil {
		return nil, nil, fmt.Errorf("sign: request %q not found", id)
	}

	files, err := s.store.ListSignFilesByRequest(ctx, id)
	if err != nil {
		return nil, nil, fmt.Errorf("sign: list sign files: %w", err)
	}

	return req, files, nil
}

// GetFileDownloadURL returns a presigned URL and the original filename for the
// signed output of the given file. If no signed key exists yet, the source key
// is used instead.
func (s *Service) GetFileDownloadURL(ctx context.Context, fileID string) (string, string, error) {
	sf, err := s.store.GetSignFile(ctx, fileID)
	if err != nil {
		return "", "", fmt.Errorf("sign: get sign file: %w", err)
	}
	if sf == nil {
		return "", "", fmt.Errorf("sign: file %q not found", fileID)
	}

	ossKey := sf.SourceOSSKey
	if sf.SignedOSSKey != nil && *sf.SignedOSSKey != "" {
		ossKey = *sf.SignedOSSKey
	}

	url, err := s.oss.GetPresignedURL(ctx, ossKey)
	if err != nil {
		return "", "", fmt.Errorf("sign: get presigned URL: %w", err)
	}

	return url, sf.OriginalName, nil
}

// GetFileReader opens a streaming reader for the signed (or source) OSS object.
// The caller is responsible for closing the returned io.ReadCloser. Returns the
// reader, the original filename, and the file size in bytes.
func (s *Service) GetFileReader(ctx context.Context, fileID string) (io.ReadCloser, string, int64, error) {
	sf, err := s.store.GetSignFile(ctx, fileID)
	if err != nil {
		return nil, "", 0, fmt.Errorf("sign: get sign file: %w", err)
	}
	if sf == nil {
		return nil, "", 0, fmt.Errorf("sign: file %q not found", fileID)
	}

	ossKey := sf.SourceOSSKey
	size := sf.SizeBytes
	if sf.SignedOSSKey != nil && *sf.SignedOSSKey != "" {
		ossKey = *sf.SignedOSSKey
		if sf.SignedSizeBytes != nil {
			size = *sf.SignedSizeBytes
		}
	}

	rc, err := s.oss.GetObject(ctx, ossKey)
	if err != nil {
		return nil, "", 0, fmt.Errorf("sign: get object: %w", err)
	}

	return rc, sf.OriginalName, size, nil
}

// ---------------------------------------------------------------------------
// Signed file upload – multipart (web / admin path)
// ---------------------------------------------------------------------------

// InitSignedUpload initialises a multipart upload for the admin to upload the
// signed version of a file. Returns the draft ID and recommended chunk size.
func (s *Service) InitSignedUpload(ctx context.Context, fileID, createdBy string, fileSize int64) (*UploadInitResponse, error) {
	sf, err := s.store.GetSignFile(ctx, fileID)
	if err != nil {
		return nil, fmt.Errorf("sign: get sign file: %w", err)
	}
	if sf == nil {
		return nil, fmt.Errorf("sign: file %q not found", fileID)
	}

	ossKey := s.oss.FullKey(ossstore.SignSignedKey(sf.RequestID, sf.ID, sf.OriginalName))

	uploadID, err := s.oss.InitMultipartUpload(ctx, ossKey)
	if err != nil {
		return nil, fmt.Errorf("sign: init multipart upload for signed file: %w", err)
	}

	draftID := auth.GenerateULID()
	now := time.Now().UTC()
	draft := &model.UploadDraft{
		ID:            draftID,
		CreatedBy:     createdBy,
		OSSUploadID:   uploadID,
		OSSKey:        ossKey,
		TotalSize:     fileSize,
		UploadedParts: "[]",
		ExpiresAt:     now.Add(7 * 24 * time.Hour),
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	if err := s.store.CreateUploadDraft(ctx, draft); err != nil {
		_ = s.oss.AbortMultipartUpload(ctx, ossKey, uploadID)
		return nil, fmt.Errorf("sign: create upload draft for signed file: %w", err)
	}

	return &UploadInitResponse{
		DraftID:   draftID,
		ChunkSize: s.chunkSize,
	}, nil
}

// CompleteSignedUpload finalises the multipart upload of a signed file,
// updates the sign_file record with the signed OSS key and size, and
// transitions the parent request to done if no pending files remain.
func (s *Service) CompleteSignedUpload(ctx context.Context, draftID, fileID string) error {
	draft, err := s.store.GetUploadDraft(ctx, draftID)
	if err != nil {
		return fmt.Errorf("sign: get upload draft: %w", err)
	}
	if draft == nil {
		return fmt.Errorf("sign: upload draft %q not found", draftID)
	}

	var parts []ossstore.UploadPart
	if draft.UploadedParts != "" && draft.UploadedParts != "[]" {
		if err := json.Unmarshal([]byte(draft.UploadedParts), &parts); err != nil {
			return fmt.Errorf("sign: parse uploaded parts: %w", err)
		}
	}

	if err := s.oss.CompleteMultipartUpload(ctx, draft.OSSKey, draft.OSSUploadID, parts); err != nil {
		return fmt.Errorf("sign: complete multipart upload for signed file: %w", err)
	}

	if err := s.store.UpdateSignFileSigned(ctx, fileID, draft.OSSKey, draft.TotalSize); err != nil {
		return fmt.Errorf("sign: update sign file signed: %w", err)
	}

	if err := s.store.DeleteUploadDraft(ctx, draftID); err != nil {
		_ = err // best-effort
	}

	sf, err := s.store.GetSignFile(ctx, fileID)
	if err != nil {
		return fmt.Errorf("sign: get sign file after update: %w", err)
	}
	if sf == nil {
		return fmt.Errorf("sign: file %q not found after update", fileID)
	}

	return s.checkAndUpdateRequestStatus(ctx, sf.RequestID)
}

// ---------------------------------------------------------------------------
// Signed file upload – direct PUT (CLI / small files)
// ---------------------------------------------------------------------------

// UploadSignedFileDirect uploads a signed file directly to OSS using a single
// PutObject request (intended for small files from the CLI) and updates the
// sign_file record. It then checks whether the parent request is complete.
func (s *Service) UploadSignedFileDirect(ctx context.Context, fileID, createdBy string, reader io.Reader, size int64) error {
	sf, err := s.store.GetSignFile(ctx, fileID)
	if err != nil {
		return fmt.Errorf("sign: get sign file: %w", err)
	}
	if sf == nil {
		return fmt.Errorf("sign: file %q not found", fileID)
	}

	ossKey := s.oss.FullKey(ossstore.SignSignedKey(sf.RequestID, sf.ID, sf.OriginalName))

	if err := s.oss.PutObject(ctx, ossKey, reader, size); err != nil {
		return fmt.Errorf("sign: put signed object: %w", err)
	}

	if err := s.store.UpdateSignFileSigned(ctx, fileID, ossKey, size); err != nil {
		return fmt.Errorf("sign: update sign file signed: %w", err)
	}

	return s.checkAndUpdateRequestStatus(ctx, sf.RequestID)
}

// ---------------------------------------------------------------------------
// Failure handling
// ---------------------------------------------------------------------------

// MarkFileFailed marks the given sign file as canceled with a failure reason
// and checks whether the parent request should be transitioned to done.
func (s *Service) MarkFileFailed(ctx context.Context, fileID, reason string) error {
	sf, err := s.store.GetSignFile(ctx, fileID)
	if err != nil {
		return fmt.Errorf("sign: get sign file: %w", err)
	}
	if sf == nil {
		return fmt.Errorf("sign: file %q not found", fileID)
	}

	if err := s.store.UpdateSignFileFailed(ctx, fileID, reason); err != nil {
		return fmt.Errorf("sign: mark file failed: %w", err)
	}

	return s.checkAndUpdateRequestStatus(ctx, sf.RequestID)
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// checkAndUpdateRequestStatus counts the file statuses for requestID and, if
// no files remain in the pending state, updates the request status to done.
func (s *Service) checkAndUpdateRequestStatus(ctx context.Context, requestID string) error {
	total, pending, err := s.store.CountSignFileStatuses(ctx, requestID)
	if err != nil {
		return fmt.Errorf("sign: count file statuses: %w", err)
	}

	if total > 0 && pending == 0 {
		if err := s.store.UpdateSignRequestStatus(ctx, requestID, model.SignRequestDone); err != nil {
			return fmt.Errorf("sign: update request status to done: %w", err)
		}
	}

	return nil
}

// extractOriginalName derives the original filename from an OSS key that was
// built by ossstore.SignSourceKey or ossstore.SignSignedKey.
//
// The key pattern is:
//
//	[prefix/]sign/<requestID>/source/<fileID>-<originalName>
//	[prefix/]sign/<requestID>/signed/<fileID>-<originalName>
//
// We take the base component and strip the leading "<fileID>-" segment.
func extractOriginalName(ossKey string) string {
	base := filepath.Base(ossKey)
	// The fileID is a ULID (26 characters). The separator is "-".
	const ulidLen = 26
	if len(base) > ulidLen+1 && base[ulidLen] == '-' {
		return base[ulidLen+1:]
	}
	// Fallback: return the base as-is.
	return base
}
