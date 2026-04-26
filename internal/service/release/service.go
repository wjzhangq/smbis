package release

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/wj/smbis/internal/auth"
	"github.com/wj/smbis/internal/model"
	"github.com/wj/smbis/internal/store/oss"
	ossstore "github.com/wj/smbis/internal/store/oss"
)

// Store defines the persistence operations required by the release service.
type Store interface {
	CreateReleaseRequest(ctx context.Context, r *model.ReleaseRequest) (bool, error)
	GetReleaseRequest(ctx context.Context, id string) (*model.ReleaseRequest, error)
	ListReleaseRequestsByUser(ctx context.Context, userID string, limit int) ([]model.ReleaseRequest, error)
	ListAllReleaseRequests(ctx context.Context, limit int) ([]model.ReleaseRequest, error)
	MarkReleaseRequestDone(ctx context.Context, id string) error
	CreateReleaseFile(ctx context.Context, f *model.ReleaseFile) error
	ListReleaseFilesByRequest(ctx context.Context, requestID string) ([]model.ReleaseFile, error)
	CreateReleaseVerification(ctx context.Context, v *model.ReleaseVerification) error
	ListVerificationsByRequest(ctx context.Context, requestID string) ([]model.ReleaseVerification, error)
	CreateUploadDraft(ctx context.Context, d *model.UploadDraft) error
	GetUploadDraft(ctx context.Context, id string) (*model.UploadDraft, error)
	UpdateUploadDraftParts(ctx context.Context, id, partsJSON string) error
	DeleteUploadDraft(ctx context.Context, id string) error
}

// OSSClient defines the object-storage operations required by the release service.
type OSSClient interface {
	FullKey(key string) string
	InitMultipartUpload(ctx context.Context, ossKey string) (string, error)
	UploadPart(ctx context.Context, ossKey, uploadID string, partNumber int, reader io.Reader, size int64) (string, error)
	CompleteMultipartUpload(ctx context.Context, ossKey, uploadID string, parts []ossstore.UploadPart) error
	AbortMultipartUpload(ctx context.Context, ossKey, uploadID string) error
	GetPresignedURL(ctx context.Context, ossKey string) (string, error)
	GetObject(ctx context.Context, ossKey string) (io.ReadCloser, error)
}

// UploadInitResponse is returned when a multipart upload is initialised.
type UploadInitResponse struct {
	DraftID   string `json:"draft_id"`
	ChunkSize int64  `json:"chunk_size"`
}

// Service implements the release request business logic.
type Service struct {
	store           Store
	oss             OSSClient
	chunkSize       int64
	maxFileSize     int64
	httpTimeout     time.Duration
	followRedirects int
}

// New creates a new Service.
func New(store Store, ossClient OSSClient, chunkSize, maxFileSize int64, httpTimeout time.Duration, followRedirects int) *Service {
	return &Service{
		store:           store,
		oss:             ossClient,
		chunkSize:       chunkSize,
		maxFileSize:     maxFileSize,
		httpTimeout:     httpTimeout,
		followRedirects: followRedirects,
	}
}

// CreateRequest creates a new release request after validating the name and
// expected URL. It retries ID generation up to 5 times to handle collisions.
func (s *Service) CreateRequest(ctx context.Context, name, userID, expectedURL string) (*model.ReleaseRequest, error) {
	if len(name) < 1 || len(name) > 100 {
		return nil, errors.New("release: name must be between 1 and 100 characters")
	}

	parsed, err := url.Parse(expectedURL)
	if err != nil {
		return nil, fmt.Errorf("release: invalid expected_url: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("release: expected_url must use http or https scheme")
	}
	if parsed.Host == "" {
		return nil, errors.New("release: expected_url must have a host")
	}

	now := time.Now().UTC()

	const maxRetries = 5
	for attempt := 0; attempt < maxRetries; attempt++ {
		id := auth.GenerateRequestID()

		r := &model.ReleaseRequest{
			ID:          id,
			Name:        name,
			UserID:      userID,
			ExpectedURL: expectedURL,
			Status:      model.ReleaseRequestPending,
			CreatedAt:   now,
			UpdatedAt:   now,
		}

		inserted, err := s.store.CreateReleaseRequest(ctx, r)
		if err != nil {
			return nil, fmt.Errorf("release: create request: %w", err)
		}
		if inserted {
			return r, nil
		}
		// ID collision – try again with a new ID.
	}

	return nil, errors.New("release: failed to generate unique request ID after 5 attempts")
}

// InitFileUpload starts a multipart upload for a file attached to a release
// request. There is no file type restriction. It returns an UploadInitResponse
// containing the draft ID and the configured chunk size.
func (s *Service) InitFileUpload(ctx context.Context, requestID, userID, fileName string, fileSize int64) (*UploadInitResponse, error) {
	fileID := auth.GenerateULID()
	logicalKey := ossstore.ReleaseFileKey(requestID, fileID, fileName)
	ossKey := s.oss.FullKey(logicalKey)

	uploadID, err := s.oss.InitMultipartUpload(ctx, ossKey)
	if err != nil {
		return nil, fmt.Errorf("release: init multipart upload: %w", err)
	}

	draftID := auth.GenerateULID()
	now := time.Now().UTC()
	draft := &model.UploadDraft{
		ID:            draftID,
		CreatedBy:     userID,
		OSSUploadID:   uploadID,
		OSSKey:        ossKey,
		TotalSize:     fileSize,
		UploadedParts: "[]",
		ExpiresAt:     now.Add(24 * time.Hour),
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	if err := s.store.CreateUploadDraft(ctx, draft); err != nil {
		// Best-effort abort to avoid leaking the incomplete multipart upload.
		_ = s.oss.AbortMultipartUpload(ctx, ossKey, uploadID)
		return nil, fmt.Errorf("release: create upload draft: %w", err)
	}

	return &UploadInitResponse{
		DraftID:   draftID,
		ChunkSize: s.chunkSize,
	}, nil
}

// UploadPart uploads a single part of a multipart upload identified by draftID.
// It returns the ETag assigned by OSS for the uploaded part.
func (s *Service) UploadPart(ctx context.Context, draftID string, partNumber int, reader io.Reader, size int64) (string, error) {
	draft, err := s.store.GetUploadDraft(ctx, draftID)
	if err != nil {
		return "", fmt.Errorf("release: get upload draft: %w", err)
	}
	if draft == nil {
		return "", fmt.Errorf("release: upload draft %q not found", draftID)
	}

	etag, err := s.oss.UploadPart(ctx, draft.OSSKey, draft.OSSUploadID, partNumber, reader, size)
	if err != nil {
		return "", fmt.Errorf("release: upload part %d: %w", partNumber, err)
	}

	// Deserialise existing parts, append the new one, and persist.
	var parts []oss.UploadPart
	if err := json.Unmarshal([]byte(draft.UploadedParts), &parts); err != nil {
		return "", fmt.Errorf("release: parse uploaded parts: %w", err)
	}
	parts = append(parts, oss.UploadPart{PartNumber: partNumber, ETag: etag})

	partsJSON, err := json.Marshal(parts)
	if err != nil {
		return "", fmt.Errorf("release: marshal uploaded parts: %w", err)
	}

	if err := s.store.UpdateUploadDraftParts(ctx, draftID, string(partsJSON)); err != nil {
		return "", fmt.Errorf("release: update upload draft parts: %w", err)
	}

	return etag, nil
}

// CompleteFileUpload finalises the multipart upload, creates a release_file
// record, and deletes the upload draft.
func (s *Service) CompleteFileUpload(ctx context.Context, draftID, requestID, userID string) (*model.ReleaseFile, error) {
	draft, err := s.store.GetUploadDraft(ctx, draftID)
	if err != nil {
		return nil, fmt.Errorf("release: get upload draft: %w", err)
	}
	if draft == nil {
		return nil, fmt.Errorf("release: upload draft %q not found", draftID)
	}

	var parts []ossstore.UploadPart
	if err := json.Unmarshal([]byte(draft.UploadedParts), &parts); err != nil {
		return nil, fmt.Errorf("release: parse uploaded parts: %w", err)
	}

	if err := s.oss.CompleteMultipartUpload(ctx, draft.OSSKey, draft.OSSUploadID, parts); err != nil {
		return nil, fmt.Errorf("release: complete multipart upload: %w", err)
	}

	// Derive the original file name from the OSS key:
	// The key has the form prefix/release/{requestID}/{fileID}-{originalName},
	// so the last path segment after the first '-' is the original name.
	originalName := extractOriginalName(draft.OSSKey)

	now := time.Now().UTC()
	rf := &model.ReleaseFile{
		ID:           auth.GenerateULID(),
		RequestID:    requestID,
		OriginalName: originalName,
		SizeBytes:    draft.TotalSize,
		OSSKey:       draft.OSSKey,
		CreatedAt:    now,
	}

	if err := s.store.CreateReleaseFile(ctx, rf); err != nil {
		return nil, fmt.Errorf("release: create release file record: %w", err)
	}

	if err := s.store.DeleteUploadDraft(ctx, draftID); err != nil {
		// Non-fatal: the file record is already committed.
		_ = err
	}

	return rf, nil
}

// AbortFileUpload aborts a multipart upload and removes the draft record.
func (s *Service) AbortFileUpload(ctx context.Context, draftID string) error {
	draft, err := s.store.GetUploadDraft(ctx, draftID)
	if err != nil {
		return fmt.Errorf("release: get upload draft: %w", err)
	}
	if draft == nil {
		return fmt.Errorf("release: upload draft %q not found", draftID)
	}

	if err := s.oss.AbortMultipartUpload(ctx, draft.OSSKey, draft.OSSUploadID); err != nil {
		return fmt.Errorf("release: abort multipart upload: %w", err)
	}

	if err := s.store.DeleteUploadDraft(ctx, draftID); err != nil {
		return fmt.Errorf("release: delete upload draft: %w", err)
	}

	return nil
}

// GetRequestWithFiles returns a release request and its associated files.
func (s *Service) GetRequestWithFiles(ctx context.Context, id string) (*model.ReleaseRequest, []model.ReleaseFile, error) {
	req, err := s.store.GetReleaseRequest(ctx, id)
	if err != nil {
		return nil, nil, fmt.Errorf("release: get request: %w", err)
	}
	if req == nil {
		return nil, nil, fmt.Errorf("release: request %q not found", id)
	}

	files, err := s.store.ListReleaseFilesByRequest(ctx, id)
	if err != nil {
		return nil, nil, fmt.Errorf("release: list files: %w", err)
	}

	return req, files, nil
}

// MarkDone marks a release request as done.
func (s *Service) MarkDone(ctx context.Context, id string) error {
	if err := s.store.MarkReleaseRequestDone(ctx, id); err != nil {
		return fmt.Errorf("release: mark done: %w", err)
	}
	return nil
}

// VerifyURL performs an HTTP GET against the release request's expected URL,
// records the result (reachability, HTTP status, latency, error message) in the
// release_verifications table, and returns the persisted record.
func (s *Service) VerifyURL(ctx context.Context, requestID string) (*model.ReleaseVerification, error) {
	req, err := s.store.GetReleaseRequest(ctx, requestID)
	if err != nil {
		return nil, fmt.Errorf("release: get request: %w", err)
	}
	if req == nil {
		return nil, fmt.Errorf("release: request %q not found", requestID)
	}

	redirectsFollowed := 0
	maxRedirects := s.followRedirects
	client := &http.Client{
		Timeout: s.httpTimeout,
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			redirectsFollowed++
			if redirectsFollowed > maxRedirects {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}

	start := time.Now()
	resp, httpErr := client.Get(req.ExpectedURL)
	latency := time.Since(start)
	latencyMs := int(latency.Milliseconds())

	now := time.Now().UTC()
	v := &model.ReleaseVerification{
		ID:         auth.GenerateULID(),
		RequestID:  requestID,
		LatencyMs:  &latencyMs,
		VerifiedAt: now,
	}

	if httpErr != nil {
		errStr := httpErr.Error()
		v.Reachable = false
		v.Error = &errStr
	} else {
		resp.Body.Close()
		status := resp.StatusCode
		v.HTTPStatus = &status
		// Treat 2xx and 3xx as reachable.
		v.Reachable = status >= 200 && status < 400
	}

	if err := s.store.CreateReleaseVerification(ctx, v); err != nil {
		return nil, fmt.Errorf("release: save verification: %w", err)
	}

	return v, nil
}

// GetFileDownloadReader returns a streaming reader and the original file name
// for the release file identified by fileID within requestID.
func (s *Service) GetFileDownloadReader(ctx context.Context, requestID, fileID string) (io.ReadCloser, string, error) {
	files, err := s.store.ListReleaseFilesByRequest(ctx, requestID)
	if err != nil {
		return nil, "", fmt.Errorf("release: list files: %w", err)
	}

	var target *model.ReleaseFile
	for i := range files {
		if files[i].ID == fileID {
			target = &files[i]
			break
		}
	}
	if target == nil {
		return nil, "", fmt.Errorf("release: file %q not found in request %q", fileID, requestID)
	}

	rc, err := s.oss.GetObject(ctx, target.OSSKey)
	if err != nil {
		return nil, "", fmt.Errorf("release: get object from OSS: %w", err)
	}

	return rc, target.OriginalName, nil
}

// ListVerifications returns all URL verification records for the given request.
func (s *Service) ListVerifications(ctx context.Context, requestID string) ([]model.ReleaseVerification, error) {
	vs, err := s.store.ListVerificationsByRequest(ctx, requestID)
	if err != nil {
		return nil, fmt.Errorf("release: list verifications: %w", err)
	}
	return vs, nil
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// extractOriginalName derives the original file name from an OSS key of the form
// .../{fileID}-{originalName}. If the expected separator is not found it returns
// the full last path segment.
func extractOriginalName(ossKey string) string {
	// Find the last '/' to isolate the file segment.
	segment := ossKey
	for i := len(ossKey) - 1; i >= 0; i-- {
		if ossKey[i] == '/' {
			segment = ossKey[i+1:]
			break
		}
	}
	// The segment is "{fileID}-{originalName}"; skip past the first '-'.
	for i := 0; i < len(segment); i++ {
		if segment[i] == '-' {
			return segment[i+1:]
		}
	}
	return segment
}
