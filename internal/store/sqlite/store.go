package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/wj/smbis/internal/model"
	"github.com/wj/smbis/migrations"
)

// Store is the SQLite-backed data access layer.
type Store struct {
	db *sql.DB
}

// New opens the SQLite database at dbPath, applies WAL mode and pragma settings,
// limits connections to 1, then runs all pending migrations.
func New(dbPath string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, fmt.Errorf("sqlite: create db directory: %w", err)
	}

	dsn := fmt.Sprintf(
		"file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)",
		dbPath,
	)

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlite: open: %w", err)
	}

	db.SetMaxOpenConns(1)

	if err := runMigrations(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite: migrations: %w", err)
	}

	return &Store{db: db}, nil
}

// runMigrations reads all *.sql files from migrations.FS in sorted order and
// executes them against db.
func runMigrations(db *sql.DB) error {
	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}

	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)

	for _, name := range files {
		data, err := fs.ReadFile(migrations.FS, name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		if _, err := db.Exec(string(data)); err != nil {
			return fmt.Errorf("exec migration %s: %w", name, err)
		}
	}
	return nil
}

// Close releases the underlying database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

func parseTime(s string) (time.Time, error) {
	return time.Parse(time.RFC3339, s)
}

func parseTimePtr(s *string) (*time.Time, error) {
	if s == nil {
		return nil, nil
	}
	t, err := parseTime(*s)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// ---------------------------------------------------------------------------
// User CRUD
// ---------------------------------------------------------------------------

// CreateUser inserts a new user record.
func (s *Store) CreateUser(ctx context.Context, user *model.User) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO users (id, username, password, disabled, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		user.ID,
		user.Username,
		user.Password,
		boolToInt(user.Disabled),
		formatTime(user.CreatedAt),
		formatTime(user.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("CreateUser: %w", err)
	}
	return nil
}

// GetUserByUsername returns the user with the given username, or nil if not found.
func (s *Store) GetUserByUsername(ctx context.Context, username string) (*model.User, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, username, password, disabled, created_at, updated_at
		 FROM users WHERE username = ?`, username)
	return scanUser(row)
}

// GetUserByID returns the user with the given ID, or nil if not found.
func (s *Store) GetUserByID(ctx context.Context, id string) (*model.User, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, username, password, disabled, created_at, updated_at
		 FROM users WHERE id = ?`, id)
	return scanUser(row)
}

// ListUsers returns all users.
func (s *Store) ListUsers(ctx context.Context) ([]model.User, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, username, password, disabled, created_at, updated_at
		 FROM users ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("ListUsers: %w", err)
	}
	defer rows.Close()

	var users []model.User
	for rows.Next() {
		u, err := scanUserRows(rows)
		if err != nil {
			return nil, fmt.Errorf("ListUsers scan: %w", err)
		}
		users = append(users, *u)
	}
	return users, rows.Err()
}

// UpdateUserPassword updates the hashed password for the given user ID.
func (s *Store) UpdateUserPassword(ctx context.Context, id, password string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET password = ?, updated_at = ? WHERE id = ?`,
		password, formatTime(time.Now().UTC()), id,
	)
	if err != nil {
		return fmt.Errorf("UpdateUserPassword: %w", err)
	}
	return nil
}

// SetUserDisabled enables or disables the user account.
func (s *Store) SetUserDisabled(ctx context.Context, id string, disabled bool) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET disabled = ?, updated_at = ? WHERE id = ?`,
		boolToInt(disabled), formatTime(time.Now().UTC()), id,
	)
	if err != nil {
		return fmt.Errorf("SetUserDisabled: %w", err)
	}
	return nil
}

func scanUser(row *sql.Row) (*model.User, error) {
	var u model.User
	var disabledInt int
	var createdAt, updatedAt string
	err := row.Scan(&u.ID, &u.Username, &u.Password, &disabledInt, &createdAt, &updatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scanUser: %w", err)
	}
	u.Disabled = disabledInt != 0
	if u.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, fmt.Errorf("scanUser created_at: %w", err)
	}
	if u.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return nil, fmt.Errorf("scanUser updated_at: %w", err)
	}
	return &u, nil
}

func scanUserRows(rows *sql.Rows) (*model.User, error) {
	var u model.User
	var disabledInt int
	var createdAt, updatedAt string
	err := rows.Scan(&u.ID, &u.Username, &u.Password, &disabledInt, &createdAt, &updatedAt)
	if err != nil {
		return nil, fmt.Errorf("scanUserRows: %w", err)
	}
	u.Disabled = disabledInt != 0
	if u.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, fmt.Errorf("scanUserRows created_at: %w", err)
	}
	if u.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return nil, fmt.Errorf("scanUserRows updated_at: %w", err)
	}
	return &u, nil
}

// ---------------------------------------------------------------------------
// Session CRUD
// ---------------------------------------------------------------------------

// CreateSession inserts a new session record.
func (s *Store) CreateSession(ctx context.Context, sess *model.Session) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (id, user_id, username, is_admin, expires_at, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		sess.ID,
		sess.UserID,
		sess.Username,
		boolToInt(sess.IsAdmin),
		formatTime(sess.ExpiresAt),
		formatTime(sess.CreatedAt),
	)
	if err != nil {
		return fmt.Errorf("CreateSession: %w", err)
	}
	return nil
}

// GetSession returns a non-expired session by ID, or nil if not found/expired.
func (s *Store) GetSession(ctx context.Context, id string) (*model.Session, error) {
	now := formatTime(time.Now().UTC())
	row := s.db.QueryRowContext(ctx,
		`SELECT id, user_id, username, is_admin, expires_at, created_at
		 FROM sessions WHERE id = ? AND expires_at > ?`, id, now)

	var sess model.Session
	var isAdminInt int
	var expiresAt, createdAt string
	err := row.Scan(&sess.ID, &sess.UserID, &sess.Username, &isAdminInt, &expiresAt, &createdAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("GetSession: %w", err)
	}
	sess.IsAdmin = isAdminInt != 0
	if sess.ExpiresAt, err = parseTime(expiresAt); err != nil {
		return nil, fmt.Errorf("GetSession expires_at: %w", err)
	}
	if sess.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, fmt.Errorf("GetSession created_at: %w", err)
	}
	return &sess, nil
}

// DeleteSession removes a session by ID.
func (s *Store) DeleteSession(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("DeleteSession: %w", err)
	}
	return nil
}

// CleanExpiredSessions deletes all sessions whose expires_at is in the past
// and returns the number of rows deleted.
func (s *Store) CleanExpiredSessions(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM sessions WHERE expires_at <= ?`, formatTime(time.Now().UTC()))
	if err != nil {
		return 0, fmt.Errorf("CleanExpiredSessions: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("CleanExpiredSessions rows affected: %w", err)
	}
	return n, nil
}

// ---------------------------------------------------------------------------
// CLI Key CRUD
// ---------------------------------------------------------------------------

// CreateCLIKey inserts a new CLI key.
func (s *Store) CreateCLIKey(ctx context.Context, k *model.CLIKey) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO cli_keys (id, name, key_value, last_used_at, revoked_at, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		k.ID,
		k.Name,
		k.KeyValue,
		timeToStringPtr(k.LastUsedAt),
		timeToStringPtr(k.RevokedAt),
		formatTime(k.CreatedAt),
	)
	if err != nil {
		return fmt.Errorf("CreateCLIKey: %w", err)
	}
	return nil
}

// GetCLIKeyByValue returns a non-revoked CLI key by its key value, or nil.
func (s *Store) GetCLIKeyByValue(ctx context.Context, keyValue string) (*model.CLIKey, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, key_value, last_used_at, revoked_at, created_at
		 FROM cli_keys WHERE key_value = ? AND revoked_at IS NULL`, keyValue)
	return scanCLIKey(row)
}

// ListCLIKeys returns all CLI keys including revoked ones.
func (s *Store) ListCLIKeys(ctx context.Context) ([]model.CLIKey, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, key_value, last_used_at, revoked_at, created_at
		 FROM cli_keys ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("ListCLIKeys: %w", err)
	}
	defer rows.Close()

	var keys []model.CLIKey
	for rows.Next() {
		k, err := scanCLIKeyRows(rows)
		if err != nil {
			return nil, fmt.Errorf("ListCLIKeys scan: %w", err)
		}
		keys = append(keys, *k)
	}
	return keys, rows.Err()
}

// RevokeCLIKey sets revoked_at to now for the given key ID.
func (s *Store) RevokeCLIKey(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE cli_keys SET revoked_at = ? WHERE id = ?`,
		formatTime(time.Now().UTC()), id,
	)
	if err != nil {
		return fmt.Errorf("RevokeCLIKey: %w", err)
	}
	return nil
}

// UpdateCLIKeyLastUsed sets last_used_at to now for the given key ID.
func (s *Store) UpdateCLIKeyLastUsed(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE cli_keys SET last_used_at = ? WHERE id = ?`,
		formatTime(time.Now().UTC()), id,
	)
	if err != nil {
		return fmt.Errorf("UpdateCLIKeyLastUsed: %w", err)
	}
	return nil
}

func scanCLIKey(row *sql.Row) (*model.CLIKey, error) {
	var k model.CLIKey
	var lastUsedAt, revokedAt *string
	var createdAt string
	err := row.Scan(&k.ID, &k.Name, &k.KeyValue, &lastUsedAt, &revokedAt, &createdAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scanCLIKey: %w", err)
	}
	if k.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, fmt.Errorf("scanCLIKey created_at: %w", err)
	}
	if k.LastUsedAt, err = parseTimePtr(lastUsedAt); err != nil {
		return nil, fmt.Errorf("scanCLIKey last_used_at: %w", err)
	}
	if k.RevokedAt, err = parseTimePtr(revokedAt); err != nil {
		return nil, fmt.Errorf("scanCLIKey revoked_at: %w", err)
	}
	return &k, nil
}

func scanCLIKeyRows(rows *sql.Rows) (*model.CLIKey, error) {
	var k model.CLIKey
	var lastUsedAt, revokedAt *string
	var createdAt string
	err := rows.Scan(&k.ID, &k.Name, &k.KeyValue, &lastUsedAt, &revokedAt, &createdAt)
	if err != nil {
		return nil, fmt.Errorf("scanCLIKeyRows: %w", err)
	}
	if k.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, fmt.Errorf("scanCLIKeyRows created_at: %w", err)
	}
	if k.LastUsedAt, err = parseTimePtr(lastUsedAt); err != nil {
		return nil, fmt.Errorf("scanCLIKeyRows last_used_at: %w", err)
	}
	if k.RevokedAt, err = parseTimePtr(revokedAt); err != nil {
		return nil, fmt.Errorf("scanCLIKeyRows revoked_at: %w", err)
	}
	return &k, nil
}

// ---------------------------------------------------------------------------
// Sign Request CRUD
// ---------------------------------------------------------------------------

// CreateSignRequest inserts a sign request using INSERT OR IGNORE to handle
// duplicate IDs gracefully. Returns true if the row was actually inserted.
func (s *Store) CreateSignRequest(ctx context.Context, r *model.SignRequest) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO sign_requests (id, name, user_id, status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		r.ID, r.Name, r.UserID, r.Status,
		formatTime(r.CreatedAt), formatTime(r.UpdatedAt),
	)
	if err != nil {
		return false, fmt.Errorf("CreateSignRequest: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("CreateSignRequest rows affected: %w", err)
	}
	return n > 0, nil
}

// GetSignRequest returns a sign request by ID, or nil if not found.
func (s *Store) GetSignRequest(ctx context.Context, id string) (*model.SignRequest, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, user_id, status, created_at, updated_at
		 FROM sign_requests WHERE id = ?`, id)
	return scanSignRequest(row)
}

// ListSignRequestsByUser returns the most recent sign requests for a user.
func (s *Store) ListSignRequestsByUser(ctx context.Context, userID string, limit int) ([]model.SignRequest, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, user_id, status, created_at, updated_at
		 FROM sign_requests WHERE user_id = ? ORDER BY updated_at DESC LIMIT ?`,
		userID, limit)
	if err != nil {
		return nil, fmt.Errorf("ListSignRequestsByUser: %w", err)
	}
	defer rows.Close()
	return scanSignRequests(rows)
}

// ListAllSignRequests returns all sign requests for admin use.
func (s *Store) ListAllSignRequests(ctx context.Context, limit int) ([]model.SignRequest, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, user_id, status, created_at, updated_at
		 FROM sign_requests ORDER BY updated_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("ListAllSignRequests: %w", err)
	}
	defer rows.Close()
	return scanSignRequests(rows)
}

// UpdateSignRequestStatus updates the status and updated_at of a sign request.
func (s *Store) UpdateSignRequestStatus(ctx context.Context, id, status string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE sign_requests SET status = ?, updated_at = ? WHERE id = ?`,
		status, formatTime(time.Now().UTC()), id,
	)
	if err != nil {
		return fmt.Errorf("UpdateSignRequestStatus: %w", err)
	}
	return nil
}

func scanSignRequest(row *sql.Row) (*model.SignRequest, error) {
	var r model.SignRequest
	var createdAt, updatedAt string
	err := row.Scan(&r.ID, &r.Name, &r.UserID, &r.Status, &createdAt, &updatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scanSignRequest: %w", err)
	}
	if r.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, fmt.Errorf("scanSignRequest created_at: %w", err)
	}
	if r.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return nil, fmt.Errorf("scanSignRequest updated_at: %w", err)
	}
	return &r, nil
}

func scanSignRequests(rows *sql.Rows) ([]model.SignRequest, error) {
	var list []model.SignRequest
	for rows.Next() {
		var r model.SignRequest
		var createdAt, updatedAt string
		err := rows.Scan(&r.ID, &r.Name, &r.UserID, &r.Status, &createdAt, &updatedAt)
		if err != nil {
			return nil, fmt.Errorf("scanSignRequests: %w", err)
		}
		if r.CreatedAt, err = parseTime(createdAt); err != nil {
			return nil, fmt.Errorf("scanSignRequests created_at: %w", err)
		}
		if r.UpdatedAt, err = parseTime(updatedAt); err != nil {
			return nil, fmt.Errorf("scanSignRequests updated_at: %w", err)
		}
		list = append(list, r)
	}
	return list, rows.Err()
}

// ---------------------------------------------------------------------------
// Sign File CRUD
// ---------------------------------------------------------------------------

// CreateSignFile inserts a sign file record.
func (s *Store) CreateSignFile(ctx context.Context, f *model.SignFile) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sign_files
		   (id, request_id, original_name, file_type, size_bytes,
		    source_oss_key, signed_oss_key, signed_size_bytes,
		    status, fail_reason, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		f.ID, f.RequestID, f.OriginalName, f.FileType, f.SizeBytes,
		f.SourceOSSKey, f.SignedOSSKey, f.SignedSizeBytes,
		f.Status, f.FailReason,
		formatTime(f.CreatedAt), formatTime(f.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("CreateSignFile: %w", err)
	}
	return nil
}

// GetSignFile returns a sign file by ID, or nil if not found.
func (s *Store) GetSignFile(ctx context.Context, id string) (*model.SignFile, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, request_id, original_name, file_type, size_bytes,
		        source_oss_key, signed_oss_key, signed_size_bytes,
		        status, fail_reason, created_at, updated_at
		 FROM sign_files WHERE id = ?`, id)
	return scanSignFile(row)
}

// ListSignFilesByRequest returns all sign files for a given request.
func (s *Store) ListSignFilesByRequest(ctx context.Context, requestID string) ([]model.SignFile, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, request_id, original_name, file_type, size_bytes,
		        source_oss_key, signed_oss_key, signed_size_bytes,
		        status, fail_reason, created_at, updated_at
		 FROM sign_files WHERE request_id = ? ORDER BY created_at ASC`, requestID)
	if err != nil {
		return nil, fmt.Errorf("ListSignFilesByRequest: %w", err)
	}
	defer rows.Close()
	return scanSignFiles(rows)
}

// UpdateSignFileSigned marks a sign file as signed and records the output OSS key and size.
func (s *Store) UpdateSignFileSigned(ctx context.Context, id, signedOSSKey string, signedSize int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE sign_files
		 SET signed_oss_key = ?, signed_size_bytes = ?, status = ?, updated_at = ?
		 WHERE id = ?`,
		signedOSSKey, signedSize, model.SignFileStatusSigned,
		formatTime(time.Now().UTC()), id,
	)
	if err != nil {
		return fmt.Errorf("UpdateSignFileSigned: %w", err)
	}
	return nil
}

// UpdateSignFileFailed marks a sign file as canceled with a failure reason.
func (s *Store) UpdateSignFileFailed(ctx context.Context, id, reason string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE sign_files SET fail_reason = ?, status = ?, updated_at = ? WHERE id = ?`,
		reason, model.SignFileStatusCanceled, formatTime(time.Now().UTC()), id,
	)
	if err != nil {
		return fmt.Errorf("UpdateSignFileFailed: %w", err)
	}
	return nil
}

// CountSignFileStatuses returns the total file count and the pending count for
// the given request. Used to determine whether a sign request is complete.
func (s *Store) CountSignFileStatuses(ctx context.Context, requestID string) (total, pending int, err error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT
		   COUNT(*),
		   SUM(CASE WHEN status = 'pending' THEN 1 ELSE 0 END)
		 FROM sign_files WHERE request_id = ?`, requestID)
	var pendingNull sql.NullInt64
	if err = row.Scan(&total, &pendingNull); err != nil {
		return 0, 0, fmt.Errorf("CountSignFileStatuses: %w", err)
	}
	pending = int(pendingNull.Int64)
	return total, pending, nil
}

func scanSignFile(row *sql.Row) (*model.SignFile, error) {
	var f model.SignFile
	var createdAt, updatedAt string
	err := row.Scan(
		&f.ID, &f.RequestID, &f.OriginalName, &f.FileType, &f.SizeBytes,
		&f.SourceOSSKey, &f.SignedOSSKey, &f.SignedSizeBytes,
		&f.Status, &f.FailReason, &createdAt, &updatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scanSignFile: %w", err)
	}
	if f.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, fmt.Errorf("scanSignFile created_at: %w", err)
	}
	if f.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return nil, fmt.Errorf("scanSignFile updated_at: %w", err)
	}
	return &f, nil
}

func scanSignFiles(rows *sql.Rows) ([]model.SignFile, error) {
	var list []model.SignFile
	for rows.Next() {
		var f model.SignFile
		var createdAt, updatedAt string
		err := rows.Scan(
			&f.ID, &f.RequestID, &f.OriginalName, &f.FileType, &f.SizeBytes,
			&f.SourceOSSKey, &f.SignedOSSKey, &f.SignedSizeBytes,
			&f.Status, &f.FailReason, &createdAt, &updatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scanSignFiles: %w", err)
		}
		if f.CreatedAt, err = parseTime(createdAt); err != nil {
			return nil, fmt.Errorf("scanSignFiles created_at: %w", err)
		}
		if f.UpdatedAt, err = parseTime(updatedAt); err != nil {
			return nil, fmt.Errorf("scanSignFiles updated_at: %w", err)
		}
		list = append(list, f)
	}
	return list, rows.Err()
}

// ---------------------------------------------------------------------------
// Release Request CRUD
// ---------------------------------------------------------------------------

// CreateReleaseRequest inserts a release request using INSERT OR IGNORE.
// Returns true if the row was actually inserted.
func (s *Store) CreateReleaseRequest(ctx context.Context, r *model.ReleaseRequest) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO release_requests
		   (id, name, user_id, expected_url, status, created_at, updated_at, done_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.Name, r.UserID, r.ExpectedURL, r.Status,
		formatTime(r.CreatedAt), formatTime(r.UpdatedAt),
		timeToStringPtr(r.DoneAt),
	)
	if err != nil {
		return false, fmt.Errorf("CreateReleaseRequest: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("CreateReleaseRequest rows affected: %w", err)
	}
	return n > 0, nil
}

// GetReleaseRequest returns a release request by ID, or nil if not found.
func (s *Store) GetReleaseRequest(ctx context.Context, id string) (*model.ReleaseRequest, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, user_id, expected_url, status, created_at, updated_at, done_at
		 FROM release_requests WHERE id = ?`, id)
	return scanReleaseRequest(row)
}

// ListReleaseRequestsByUser returns recent release requests for a user.
func (s *Store) ListReleaseRequestsByUser(ctx context.Context, userID string, limit int) ([]model.ReleaseRequest, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, user_id, expected_url, status, created_at, updated_at, done_at
		 FROM release_requests WHERE user_id = ? ORDER BY updated_at DESC LIMIT ?`,
		userID, limit)
	if err != nil {
		return nil, fmt.Errorf("ListReleaseRequestsByUser: %w", err)
	}
	defer rows.Close()
	return scanReleaseRequests(rows)
}

// ListAllReleaseRequests returns all release requests for admin use.
func (s *Store) ListAllReleaseRequests(ctx context.Context, limit int) ([]model.ReleaseRequest, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, user_id, expected_url, status, created_at, updated_at, done_at
		 FROM release_requests ORDER BY updated_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("ListAllReleaseRequests: %w", err)
	}
	defer rows.Close()
	return scanReleaseRequests(rows)
}

// MarkReleaseRequestDone sets status=done and done_at=now for the given request.
func (s *Store) MarkReleaseRequestDone(ctx context.Context, id string) error {
	now := formatTime(time.Now().UTC())
	_, err := s.db.ExecContext(ctx,
		`UPDATE release_requests SET status = ?, updated_at = ?, done_at = ? WHERE id = ?`,
		model.ReleaseRequestDone, now, now, id,
	)
	if err != nil {
		return fmt.Errorf("MarkReleaseRequestDone: %w", err)
	}
	return nil
}

func scanReleaseRequest(row *sql.Row) (*model.ReleaseRequest, error) {
	var r model.ReleaseRequest
	var createdAt, updatedAt string
	var doneAt *string
	err := row.Scan(&r.ID, &r.Name, &r.UserID, &r.ExpectedURL, &r.Status,
		&createdAt, &updatedAt, &doneAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scanReleaseRequest: %w", err)
	}
	if r.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, fmt.Errorf("scanReleaseRequest created_at: %w", err)
	}
	if r.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return nil, fmt.Errorf("scanReleaseRequest updated_at: %w", err)
	}
	if r.DoneAt, err = parseTimePtr(doneAt); err != nil {
		return nil, fmt.Errorf("scanReleaseRequest done_at: %w", err)
	}
	return &r, nil
}

func scanReleaseRequests(rows *sql.Rows) ([]model.ReleaseRequest, error) {
	var list []model.ReleaseRequest
	for rows.Next() {
		var r model.ReleaseRequest
		var createdAt, updatedAt string
		var doneAt *string
		err := rows.Scan(&r.ID, &r.Name, &r.UserID, &r.ExpectedURL, &r.Status,
			&createdAt, &updatedAt, &doneAt)
		if err != nil {
			return nil, fmt.Errorf("scanReleaseRequests: %w", err)
		}
		if r.CreatedAt, err = parseTime(createdAt); err != nil {
			return nil, fmt.Errorf("scanReleaseRequests created_at: %w", err)
		}
		if r.UpdatedAt, err = parseTime(updatedAt); err != nil {
			return nil, fmt.Errorf("scanReleaseRequests updated_at: %w", err)
		}
		if r.DoneAt, err = parseTimePtr(doneAt); err != nil {
			return nil, fmt.Errorf("scanReleaseRequests done_at: %w", err)
		}
		list = append(list, r)
	}
	return list, rows.Err()
}

// ---------------------------------------------------------------------------
// Release File CRUD
// ---------------------------------------------------------------------------

// CreateReleaseFile inserts a release file record.
func (s *Store) CreateReleaseFile(ctx context.Context, f *model.ReleaseFile) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO release_files (id, request_id, original_name, size_bytes, oss_key, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		f.ID, f.RequestID, f.OriginalName, f.SizeBytes, f.OSSKey,
		formatTime(f.CreatedAt),
	)
	if err != nil {
		return fmt.Errorf("CreateReleaseFile: %w", err)
	}
	return nil
}

// ListReleaseFilesByRequest returns all release files for a given request.
func (s *Store) ListReleaseFilesByRequest(ctx context.Context, requestID string) ([]model.ReleaseFile, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, request_id, original_name, size_bytes, oss_key, created_at
		 FROM release_files WHERE request_id = ? ORDER BY created_at ASC`, requestID)
	if err != nil {
		return nil, fmt.Errorf("ListReleaseFilesByRequest: %w", err)
	}
	defer rows.Close()

	var list []model.ReleaseFile
	for rows.Next() {
		var f model.ReleaseFile
		var createdAt string
		err := rows.Scan(&f.ID, &f.RequestID, &f.OriginalName, &f.SizeBytes, &f.OSSKey, &createdAt)
		if err != nil {
			return nil, fmt.Errorf("ListReleaseFilesByRequest scan: %w", err)
		}
		if f.CreatedAt, err = parseTime(createdAt); err != nil {
			return nil, fmt.Errorf("ListReleaseFilesByRequest created_at: %w", err)
		}
		list = append(list, f)
	}
	return list, rows.Err()
}

// ---------------------------------------------------------------------------
// Release Verification
// ---------------------------------------------------------------------------

// CreateReleaseVerification inserts a verification record.
func (s *Store) CreateReleaseVerification(ctx context.Context, v *model.ReleaseVerification) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO release_verifications
		   (id, request_id, reachable, http_status, latency_ms, error, verified_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		v.ID, v.RequestID, boolToInt(v.Reachable),
		v.HTTPStatus, v.LatencyMs, v.Error,
		formatTime(v.VerifiedAt),
	)
	if err != nil {
		return fmt.Errorf("CreateReleaseVerification: %w", err)
	}
	return nil
}

// ListVerificationsByRequest returns all verifications for a release request.
func (s *Store) ListVerificationsByRequest(ctx context.Context, requestID string) ([]model.ReleaseVerification, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, request_id, reachable, http_status, latency_ms, error, verified_at
		 FROM release_verifications WHERE request_id = ? ORDER BY verified_at ASC`, requestID)
	if err != nil {
		return nil, fmt.Errorf("ListVerificationsByRequest: %w", err)
	}
	defer rows.Close()

	var list []model.ReleaseVerification
	for rows.Next() {
		var v model.ReleaseVerification
		var reachableInt int
		var verifiedAt string
		err := rows.Scan(&v.ID, &v.RequestID, &reachableInt,
			&v.HTTPStatus, &v.LatencyMs, &v.Error, &verifiedAt)
		if err != nil {
			return nil, fmt.Errorf("ListVerificationsByRequest scan: %w", err)
		}
		v.Reachable = reachableInt != 0
		if v.VerifiedAt, err = parseTime(verifiedAt); err != nil {
			return nil, fmt.Errorf("ListVerificationsByRequest verified_at: %w", err)
		}
		list = append(list, v)
	}
	return list, rows.Err()
}

// ---------------------------------------------------------------------------
// Upload Draft CRUD
// ---------------------------------------------------------------------------

// CreateUploadDraft inserts a new upload draft.
func (s *Store) CreateUploadDraft(ctx context.Context, d *model.UploadDraft) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO upload_drafts
		   (id, created_by, oss_upload_id, oss_key, total_size,
		    uploaded_parts, expires_at, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.ID, d.CreatedBy, d.OSSUploadID, d.OSSKey, d.TotalSize,
		d.UploadedParts,
		formatTime(d.ExpiresAt), formatTime(d.CreatedAt), formatTime(d.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("CreateUploadDraft: %w", err)
	}
	return nil
}

// GetUploadDraft returns an upload draft by ID, or nil if not found.
func (s *Store) GetUploadDraft(ctx context.Context, id string) (*model.UploadDraft, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, created_by, oss_upload_id, oss_key, total_size,
		        uploaded_parts, expires_at, created_at, updated_at
		 FROM upload_drafts WHERE id = ?`, id)
	return scanUploadDraft(row)
}

// UpdateUploadDraftParts updates the uploaded_parts JSON and updated_at for a draft.
func (s *Store) UpdateUploadDraftParts(ctx context.Context, id, partsJSON string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE upload_drafts SET uploaded_parts = ?, updated_at = ? WHERE id = ?`,
		partsJSON, formatTime(time.Now().UTC()), id,
	)
	if err != nil {
		return fmt.Errorf("UpdateUploadDraftParts: %w", err)
	}
	return nil
}

// AppendUploadDraftPart atomically reads the current parts, appends the new
// part, and writes the result back — all within a single transaction. This
// prevents TOCTOU issues if MaxOpenConns is ever increased above 1.
func (s *Store) AppendUploadDraftPart(ctx context.Context, id string, partNumber int, etag string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("AppendUploadDraftPart begin tx: %w", err)
	}
	defer tx.Rollback()

	var partsStr string
	if err := tx.QueryRowContext(ctx,
		`SELECT uploaded_parts FROM upload_drafts WHERE id = ?`, id,
	).Scan(&partsStr); err != nil {
		return fmt.Errorf("AppendUploadDraftPart select: %w", err)
	}

	// Append new part entry as raw JSON to avoid import cycles.
	newEntry := fmt.Sprintf(`{"PartNumber":%d,"ETag":%q}`, partNumber, etag)
	if partsStr == "" || partsStr == "[]" {
		partsStr = "[" + newEntry + "]"
	} else {
		// Insert before the closing ']'.
		partsStr = partsStr[:len(partsStr)-1] + "," + newEntry + "]"
	}

	_, err = tx.ExecContext(ctx,
		`UPDATE upload_drafts SET uploaded_parts = ?, updated_at = ? WHERE id = ?`,
		partsStr, formatTime(time.Now().UTC()), id,
	)
	if err != nil {
		return fmt.Errorf("AppendUploadDraftPart update: %w", err)
	}

	return tx.Commit()
}

// DeleteUploadDraft removes an upload draft by ID.
func (s *Store) DeleteUploadDraft(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM upload_drafts WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("DeleteUploadDraft: %w", err)
	}
	return nil
}

// ListExpiredUploadDrafts returns all drafts whose expires_at is in the past.
func (s *Store) ListExpiredUploadDrafts(ctx context.Context) ([]model.UploadDraft, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, created_by, oss_upload_id, oss_key, total_size,
		        uploaded_parts, expires_at, created_at, updated_at
		 FROM upload_drafts WHERE expires_at <= ?`, formatTime(time.Now().UTC()))
	if err != nil {
		return nil, fmt.Errorf("ListExpiredUploadDrafts: %w", err)
	}
	defer rows.Close()

	var list []model.UploadDraft
	for rows.Next() {
		d, err := scanUploadDraftRows(rows)
		if err != nil {
			return nil, fmt.Errorf("ListExpiredUploadDrafts scan: %w", err)
		}
		list = append(list, *d)
	}
	return list, rows.Err()
}

func scanUploadDraft(row *sql.Row) (*model.UploadDraft, error) {
	var d model.UploadDraft
	var expiresAt, createdAt, updatedAt string
	err := row.Scan(
		&d.ID, &d.CreatedBy, &d.OSSUploadID, &d.OSSKey, &d.TotalSize,
		&d.UploadedParts, &expiresAt, &createdAt, &updatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scanUploadDraft: %w", err)
	}
	if d.ExpiresAt, err = parseTime(expiresAt); err != nil {
		return nil, fmt.Errorf("scanUploadDraft expires_at: %w", err)
	}
	if d.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, fmt.Errorf("scanUploadDraft created_at: %w", err)
	}
	if d.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return nil, fmt.Errorf("scanUploadDraft updated_at: %w", err)
	}
	return &d, nil
}

func scanUploadDraftRows(rows *sql.Rows) (*model.UploadDraft, error) {
	var d model.UploadDraft
	var expiresAt, createdAt, updatedAt string
	err := rows.Scan(
		&d.ID, &d.CreatedBy, &d.OSSUploadID, &d.OSSKey, &d.TotalSize,
		&d.UploadedParts, &expiresAt, &createdAt, &updatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("scanUploadDraftRows: %w", err)
	}
	if d.ExpiresAt, err = parseTime(expiresAt); err != nil {
		return nil, fmt.Errorf("scanUploadDraftRows expires_at: %w", err)
	}
	if d.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, fmt.Errorf("scanUploadDraftRows created_at: %w", err)
	}
	if d.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return nil, fmt.Errorf("scanUploadDraftRows updated_at: %w", err)
	}
	return &d, nil
}

// ---------------------------------------------------------------------------
// Recent Requests (combined sign + release for home page)
// ---------------------------------------------------------------------------

// RecentRequest is a unified view over sign_requests and release_requests,
// used to populate a combined activity feed on the home page.
type RecentRequest struct {
	Type      string    // "sign" or "release"
	ID        string
	Name      string
	Status    string
	UserID    string
	UpdatedAt time.Time
}

// ListRecentRequestsByUser returns the most recent sign and release requests
// for a specific user, ordered by updated_at DESC.
func (s *Store) ListRecentRequestsByUser(ctx context.Context, userID string, limit int) ([]RecentRequest, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT 'sign' AS type, id, name, status, user_id, updated_at
		  FROM sign_requests WHERE user_id = ?
		UNION ALL
		SELECT 'release' AS type, id, name, status, user_id, updated_at
		  FROM release_requests WHERE user_id = ?
		ORDER BY updated_at DESC
		LIMIT ?`, userID, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("ListRecentRequestsByUser: %w", err)
	}
	defer rows.Close()
	return scanRecentRequests(rows)
}

// ListAllRecentRequests returns the most recent sign and release requests
// across all users, ordered by updated_at DESC. Used by admin views.
func (s *Store) ListAllRecentRequests(ctx context.Context, limit int) ([]RecentRequest, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT 'sign' AS type, id, name, status, user_id, updated_at
		  FROM sign_requests
		UNION ALL
		SELECT 'release' AS type, id, name, status, user_id, updated_at
		  FROM release_requests
		ORDER BY updated_at DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("ListAllRecentRequests: %w", err)
	}
	defer rows.Close()
	return scanRecentRequests(rows)
}

func scanRecentRequests(rows *sql.Rows) ([]RecentRequest, error) {
	var list []RecentRequest
	for rows.Next() {
		var r RecentRequest
		var updatedAt string
		err := rows.Scan(&r.Type, &r.ID, &r.Name, &r.Status, &r.UserID, &updatedAt)
		if err != nil {
			return nil, fmt.Errorf("scanRecentRequests: %w", err)
		}
		if r.UpdatedAt, err = parseTime(updatedAt); err != nil {
			return nil, fmt.Errorf("scanRecentRequests updated_at: %w", err)
		}
		list = append(list, r)
	}
	return list, rows.Err()
}

// ---------------------------------------------------------------------------
// Small utility helpers
// ---------------------------------------------------------------------------

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func timeToStringPtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := formatTime(*t)
	return &s
}
