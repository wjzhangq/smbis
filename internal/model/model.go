package model

import "time"

// Sign request statuses
const (
	SignRequestPending  = "pending"
	SignRequestDone     = "done"
	SignRequestCanceled = "canceled"
)

// Sign file statuses
const (
	SignFileStatusPending  = "pending"
	SignFileStatusSigned   = "signed"
	SignFileStatusCanceled = "canceled"
)

// Release request statuses
const (
	ReleaseRequestPending = "pending"
	ReleaseRequestDone    = "done"
)

// User represents a system user account.
type User struct {
	ID        string    `json:"id"`
	Username  string    `json:"username"`
	Password  string    `json:"-"`
	Disabled  bool      `json:"disabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Session represents an authenticated user session.
type Session struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	Username  string    `json:"username"`
	IsAdmin   bool      `json:"is_admin"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
}

// CLIKey represents an API key used for CLI access.
type CLIKey struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	KeyValue   string     `json:"key_value"`
	LastUsedAt *time.Time `json:"last_used_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
	CreatedAt  time.Time  `json:"created_at"`
}

// SignRequest represents a code signing request.
type SignRequest struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	UserID    string    `json:"user_id"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// SignFile represents a file within a sign request.
type SignFile struct {
	ID              string     `json:"id"`
	RequestID       string     `json:"request_id"`
	OriginalName    string     `json:"original_name"`
	FileType        string     `json:"file_type"`
	SizeBytes       int64      `json:"size_bytes"`
	SourceOSSKey    string     `json:"source_oss_key"`
	SignedOSSKey    *string    `json:"signed_oss_key"`
	SignedSizeBytes *int64     `json:"signed_size_bytes"`
	Status          string     `json:"status"`
	FailReason      *string    `json:"fail_reason"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// ReleaseRequest represents a release publishing request.
type ReleaseRequest struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	UserID      string     `json:"user_id"`
	ExpectedURL string     `json:"expected_url"`
	Status      string     `json:"status"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	DoneAt      *time.Time `json:"done_at"`
}

// ReleaseFile represents a file associated with a release request.
type ReleaseFile struct {
	ID           string    `json:"id"`
	RequestID    string    `json:"request_id"`
	OriginalName string    `json:"original_name"`
	SizeBytes    int64     `json:"size_bytes"`
	OSSKey       string    `json:"oss_key"`
	CreatedAt    time.Time `json:"created_at"`
}

// ReleaseVerification represents a URL reachability check for a release.
type ReleaseVerification struct {
	ID         string     `json:"id"`
	RequestID  string     `json:"request_id"`
	Reachable  bool       `json:"reachable"`
	HTTPStatus *int       `json:"http_status"`
	LatencyMs  *int       `json:"latency_ms"`
	Error      *string    `json:"error"`
	VerifiedAt time.Time  `json:"verified_at"`
}

// UploadDraft represents an in-progress multipart OSS upload.
type UploadDraft struct {
	ID            string    `json:"id"`
	CreatedBy     string    `json:"created_by"`
	OSSUploadID   string    `json:"oss_upload_id"`
	OSSKey        string    `json:"oss_key"`
	TotalSize     int64     `json:"total_size"`
	UploadedParts string    `json:"uploaded_parts"`
	ExpiresAt     time.Time `json:"expires_at"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// Identity holds the authenticated caller's information, used in middleware context.
type Identity struct {
	IsAdmin  bool   `json:"is_admin"`
	UserID   string `json:"user_id"`
	Username string `json:"username"`
}
