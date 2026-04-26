# SMB - Sign & Release Management Platform

SMB is an internal team platform for managing **code signing requests** and **release publishing requests**. It centralizes file custody, provides traceable request IDs, and offers both a web UI and a CLI interface for automation.

## Features

- **Sign requests** -- submit PE (.exe/.dll/.sys/.ocx/.msi) or XML files for signing; admins sign via signtool/XMLDSIG and upload the signed artifacts back.
- **Release requests** -- submit release files with an expected URL; admins mark completion; users verify URL reachability in one click.
- **Chunked upload** -- large files (up to 2 GiB) are uploaded in configurable chunks with progress tracking, retry, and resume.
- **CLI interface** -- automation-friendly JSON API with Bearer token (CLI Key) auth for scripted signing workflows.
- **Single binary** -- Go binary with embedded templates and static assets; no frontend build step.

## Tech Stack

| Component | Choice |
|---|---|
| Language | Go (>= 1.22) |
| Router | [chi](https://github.com/go-chi/chi) v5 |
| Database | SQLite via [modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite) (pure Go, no CGO) |
| Object Storage | Alibaba Cloud OSS |
| Frontend | Server-side rendered `html/template` + vanilla JS |
| Config | YAML (`config.yaml`) |

## Project Structure

```
smbis/
├── cmd/smb/main.go               # Application entry point
├── internal/
│   ├── config/                    # YAML configuration loader
│   ├── auth/                      # Login, session, CLI key helpers
│   ├── handler/
│   │   ├── web/                   # Server-rendered page handlers
│   │   ├── api/                   # Browser JSON API handlers
│   │   └── cli/                   # CLI JSON API handlers
│   ├── service/
│   │   ├── sign/                  # Signing request business logic
│   │   └── release/               # Release request business logic
│   ├── store/
│   │   ├── sqlite/                # SQLite data access layer
│   │   └── oss/                   # Alibaba Cloud OSS wrapper
│   ├── model/                     # Domain entities
│   └── middleware/                # Auth, logging, recovery middleware
├── web/
│   ├── templates/                 # Go html/template files
│   └── static/                    # JS / CSS assets
├── migrations/                    # SQL migration scripts (embedded)
├── config.example.yaml            # Example configuration
├── go.mod
└── README.md
```

## Quick Start

### Prerequisites

- Go >= 1.22
- An Alibaba Cloud OSS bucket with AccessKey credentials

### Build

```bash
go build -o smb ./cmd/smb
```

### Configure

Copy and edit the example configuration:

```bash
cp config.example.yaml config.yaml
# Edit config.yaml with your OSS credentials, admin password, etc.
```

Key configuration sections:

```yaml
server:
  listen: ":8080"
  external_url: "https://smb.example.com"
  session_ttl: "12h"
  upload_chunk_size: "8MiB"
  max_file_size: "2GiB"

admin:
  username: "admin"
  password: "change-me"

storage:
  sqlite_path: "./data/db.sqlite"

oss:
  endpoint: "oss-cn-hangzhou.aliyuncs.com"
  internal_endpoint: "oss-cn-hangzhou-internal.aliyuncs.com"
  access_key_id: "LTAI..."
  access_key_secret: "..."
  bucket: "your-bucket"
  prefix: "smb/prod"
  presign_ttl: "10m"

verify:
  http_timeout: "5s"
  follow_redirects: 5
```

### Run

```bash
./smb -config config.yaml
```

The server starts on the configured listen address (default `:8080`).

## Roles

| Role | Description |
|---|---|
| **Admin** | Credentials in config file. Can manage users, CLI keys, and process all requests. Can also submit requests (user_id = `"admin"`). |
| **User** | Created by admin via the web UI. Can log in, submit requests, and view their own requests. |
| **CLI** | Uses admin-generated CLI Keys (`Authorization: Bearer smb_xxx`) for automated signing workflows. |

## Web Routes

| Path | Description |
|---|---|
| `GET /login` | Login page |
| `GET /` | Home -- quick-access cards + recent requests |
| `GET /sign/new` | New sign request form |
| `GET /sign/{id}` | Sign request detail (user view) |
| `GET /release/new` | New release request form |
| `GET /release/{id}` | Release request detail (user view) |
| `GET /admin/sign/{id}` | Admin sign detail (download/upload/fail actions) |
| `GET /admin/release/{id}` | Admin release detail (mark done) |
| `GET /admin/users` | User management |
| `GET /admin/cli-keys` | CLI key management |
| `GET /admin/all` | All requests overview |

## CLI API

| Method | Path | Auth | Description |
|---|---|---|---|
| GET | `/cli/sign/{id}/files` | None | List pending files with download URLs |
| GET | `/cli/sign/{id}/files/{fid}/download` | None | Stream-download a source file |
| POST | `/cli/sign/{id}/files/{fid}/signed` | CLI Key | Upload signed file (small, multipart/form-data) |
| POST | `/cli/sign/{id}/files/{fid}/upload/init` | CLI Key | Init chunked upload for signed file |
| POST | `/cli/sign/{id}/files/{fid}/upload/{draft}/part?n=N` | CLI Key | Upload a chunk |
| POST | `/cli/sign/{id}/files/{fid}/upload/{draft}/complete` | CLI Key | Finalize chunked upload |
| POST | `/cli/sign/{id}/files/{fid}/fail` | CLI Key | Mark file as failed |

## Deployment

### systemd

```ini
[Unit]
Description=SMB
After=network.target

[Service]
ExecStart=/opt/smb/smb -config /etc/smb/config.yaml
Restart=on-failure
User=smb
Group=smb

[Install]
WantedBy=multi-user.target
```

### Reverse Proxy (Nginx)

- Set `client_max_body_size` >= chunk size (e.g. `16m`), not total file size.
- Set `proxy_request_buffering off` to avoid buffering chunks to disk.
- Enable HTTPS (required for `Secure` cookie flag).

### File Permissions

- `config.yaml`: `0600` -- contains OSS credentials and admin password.
- SQLite database file: `0600` -- contains user data and session tokens.

### Background Cleanup

The server automatically runs:
- **Every 10 minutes**: clean expired sessions.
- **Every 24 hours**: clean expired upload drafts and abort orphaned OSS multipart uploads.

## License

Internal use only.
