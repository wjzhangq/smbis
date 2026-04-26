package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/wj/smbis/internal/model"
)

var ErrInvalidCredentials = errors.New("invalid credentials")
var ErrUserDisabled = errors.New("user is disabled")

// UserStore is the interface for looking up users during authentication.
type UserStore interface {
	GetUserByUsername(ctx context.Context, username string) (*model.User, error)
}

// SessionStore is the interface for persisting sessions.
type SessionStore interface {
	CreateSession(ctx context.Context, s *model.Session) error
}

// AdminConfig holds the static admin credentials.
type AdminConfig struct {
	Username string
	Password string
}

// GenerateSessionID returns a 32-byte random value encoded as a 64-character hex string.
func GenerateSessionID() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate session id: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// GenerateCLIKey returns a CLI key in the format "smb_<base64url-no-padding(24 random bytes)>".
func GenerateCLIKey() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate cli key: %w", err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(b)
	return "smb_" + encoded, nil
}

// GenerateULID returns a new ULID using crypto/rand as the entropy source.
func GenerateULID() string {
	return ulid.MustNew(ulid.Now(), rand.Reader).String()
}

// GenerateRequestID returns an 8-character hex string derived from 4 random bytes.
func GenerateRequestID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		// Fall back to a zero-value string rather than panicking; callers
		// use this only for tracing/logging purposes.
		return "00000000"
	}
	return hex.EncodeToString(b)
}

// Login authenticates a user against the admin config or the user store.
// Admin credentials are checked first using constant-time comparison.
func Login(ctx context.Context, store UserStore, adminCfg AdminConfig, username, password string) (*model.Identity, error) {
	// Check admin credentials using constant-time comparison to prevent timing attacks.
	usernameMatch := subtle.ConstantTimeCompare([]byte(username), []byte(adminCfg.Username))
	passwordMatch := subtle.ConstantTimeCompare([]byte(password), []byte(adminCfg.Password))
	if usernameMatch == 1 && passwordMatch == 1 {
		return &model.Identity{
			IsAdmin:  true,
			UserID:   "admin",
			Username: adminCfg.Username,
		}, nil
	}

	user, err := store.GetUserByUsername(ctx, username)
	if err != nil {
		return nil, ErrInvalidCredentials
	}
	if user == nil {
		return nil, ErrInvalidCredentials
	}

	if user.Disabled {
		return nil, ErrUserDisabled
	}

	if user.Password != password {
		return nil, ErrInvalidCredentials
	}

	return &model.Identity{
		IsAdmin:  false,
		UserID:   user.ID,
		Username: user.Username,
	}, nil
}

// CreateSessionForIdentity creates and persists a new session for the given identity.
func CreateSessionForIdentity(ctx context.Context, store SessionStore, identity *model.Identity, ttl time.Duration) (*model.Session, error) {
	sessionID, err := GenerateSessionID()
	if err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}

	now := time.Now()
	s := &model.Session{
		ID:        sessionID,
		UserID:    identity.UserID,
		Username:  identity.Username,
		IsAdmin:   identity.IsAdmin,
		ExpiresAt: now.Add(ttl),
		CreatedAt: now,
	}

	if err := store.CreateSession(ctx, s); err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}

	return s, nil
}
