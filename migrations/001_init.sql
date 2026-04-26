-- 普通用户（管理员不入库）
CREATE TABLE IF NOT EXISTS users (
  id            TEXT PRIMARY KEY,
  username      TEXT NOT NULL UNIQUE,
  password      TEXT NOT NULL,
  disabled      INTEGER NOT NULL DEFAULT 0,
  created_at    DATETIME NOT NULL,
  updated_at    DATETIME NOT NULL
);

-- 浏览器会话
CREATE TABLE IF NOT EXISTS sessions (
  id          TEXT PRIMARY KEY,
  user_id     TEXT NOT NULL,
  username    TEXT NOT NULL DEFAULT '',
  is_admin    INTEGER NOT NULL DEFAULT 0,
  expires_at  DATETIME NOT NULL,
  created_at  DATETIME NOT NULL
);

-- CLI Key（仅管理员）
CREATE TABLE IF NOT EXISTS cli_keys (
  id           TEXT PRIMARY KEY,
  name         TEXT NOT NULL,
  key_value    TEXT NOT NULL UNIQUE,
  last_used_at DATETIME,
  revoked_at   DATETIME,
  created_at   DATETIME NOT NULL
);

-- 签名申请
CREATE TABLE IF NOT EXISTS sign_requests (
  id          TEXT PRIMARY KEY,
  name        TEXT NOT NULL,
  user_id     TEXT NOT NULL,
  status      TEXT NOT NULL DEFAULT 'pending',
  created_at  DATETIME NOT NULL,
  updated_at  DATETIME NOT NULL
);

-- 签名文件
CREATE TABLE IF NOT EXISTS sign_files (
  id                TEXT PRIMARY KEY,
  request_id        TEXT NOT NULL REFERENCES sign_requests(id) ON DELETE CASCADE,
  original_name     TEXT NOT NULL,
  file_type         TEXT NOT NULL,
  size_bytes        INTEGER NOT NULL,
  source_oss_key    TEXT NOT NULL,
  signed_oss_key    TEXT,
  signed_size_bytes INTEGER,
  status            TEXT NOT NULL DEFAULT 'pending',
  fail_reason       TEXT,
  created_at        DATETIME NOT NULL,
  updated_at        DATETIME NOT NULL
);

-- 上线申请
CREATE TABLE IF NOT EXISTS release_requests (
  id            TEXT PRIMARY KEY,
  name          TEXT NOT NULL,
  user_id       TEXT NOT NULL,
  expected_url  TEXT NOT NULL,
  status        TEXT NOT NULL DEFAULT 'pending',
  created_at    DATETIME NOT NULL,
  updated_at    DATETIME NOT NULL,
  done_at       DATETIME
);

-- 上线文件
CREATE TABLE IF NOT EXISTS release_files (
  id             TEXT PRIMARY KEY,
  request_id     TEXT NOT NULL REFERENCES release_requests(id) ON DELETE CASCADE,
  original_name  TEXT NOT NULL,
  size_bytes     INTEGER NOT NULL,
  oss_key        TEXT NOT NULL,
  created_at     DATETIME NOT NULL
);

-- 上线验证记录
CREATE TABLE IF NOT EXISTS release_verifications (
  id           TEXT PRIMARY KEY,
  request_id   TEXT NOT NULL REFERENCES release_requests(id) ON DELETE CASCADE,
  reachable    INTEGER NOT NULL,
  http_status  INTEGER,
  latency_ms   INTEGER,
  error        TEXT,
  verified_at  DATETIME NOT NULL
);

-- 用于 multipart 上传的草稿
CREATE TABLE IF NOT EXISTS upload_drafts (
  id             TEXT PRIMARY KEY,
  created_by     TEXT NOT NULL,
  oss_upload_id  TEXT NOT NULL,
  oss_key        TEXT NOT NULL,
  total_size     INTEGER NOT NULL,
  uploaded_parts TEXT NOT NULL DEFAULT '[]',
  expires_at     DATETIME NOT NULL,
  created_at     DATETIME NOT NULL,
  updated_at     DATETIME NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_sign_files_request    ON sign_files(request_id);
CREATE INDEX IF NOT EXISTS idx_release_files_request ON release_files(request_id);
CREATE INDEX IF NOT EXISTS idx_sessions_expires      ON sessions(expires_at);
CREATE INDEX IF NOT EXISTS idx_upload_drafts_expires ON upload_drafts(expires_at);
