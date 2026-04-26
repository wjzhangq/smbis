// Package web implements the server-side rendered web handler for smbis.
package web

import (
	"html/template"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/wj/smbis/internal/auth"
	"github.com/wj/smbis/internal/config"
	"github.com/wj/smbis/internal/middleware"
	"github.com/wj/smbis/internal/model"
	"github.com/wj/smbis/internal/service/release"
	"github.com/wj/smbis/internal/service/sign"
	"github.com/wj/smbis/internal/store/sqlite"
)

// PageData is the top-level data passed to every template. It carries the
// authenticated identity (for the navbar), a page title, an optional error
// string, arbitrary page-specific data, and the configured external URL.
type PageData struct {
	Identity    *model.Identity
	Title       string
	Error       string
	Data        any
	ExternalURL string
}

// Handler holds the dependencies shared by all web handlers.
type Handler struct {
	templates  *template.Template
	store      *sqlite.Store
	signSvc    *sign.Service
	releaseSvc *release.Service
	cfg        *config.Config
}

// New constructs a Handler with the supplied dependencies.
func New(
	store *sqlite.Store,
	signSvc *sign.Service,
	releaseSvc *release.Service,
	cfg *config.Config,
	tmpl *template.Template,
) *Handler {
	return &Handler{
		templates:  tmpl,
		store:      store,
		signSvc:    signSvc,
		releaseSvc: releaseSvc,
		cfg:        cfg,
	}
}

// ---------------------------------------------------------------------------
// Helper methods
// ---------------------------------------------------------------------------

// render executes the named template with the provided data and status code.
// On execution error it falls back to a plain 500 response.
func (h *Handler) render(w http.ResponseWriter, name string, status int, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := h.templates.ExecuteTemplate(w, name, data); err != nil {
		slog.Error("template execution failed", "template", name, "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}
}

// isSecure returns true when the configured external URL uses the https scheme.
func (h *Handler) isSecure() bool {
	return strings.HasPrefix(h.cfg.Server.ExternalURL, "https")
}

// pageData returns a PageData populated with the identity from the request
// context and the configured external URL.
func (h *Handler) pageData(r *http.Request, title string) PageData {
	return PageData{
		Identity:    middleware.GetIdentity(r.Context()),
		Title:       title,
		ExternalURL: h.cfg.Server.ExternalURL,
	}
}

// ---------------------------------------------------------------------------
// Auth handlers
// ---------------------------------------------------------------------------

// LoginPage handles GET /login and renders the login form.
func (h *Handler) LoginPage(w http.ResponseWriter, r *http.Request) {
	pd := h.pageData(r, "Login")
	h.render(w, "login.html", http.StatusOK, pd)
}

// Login handles POST /login. It validates the submitted credentials, creates a
// session, sets the session cookie, and redirects to /. On failure the login
// page is re-rendered with an error message.
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		pd := h.pageData(r, "Login")
		pd.Error = "invalid form submission"
		h.render(w, "login.html", http.StatusBadRequest, pd)
		return
	}

	username := r.FormValue("username")
	password := r.FormValue("password")

	adminCfg := auth.AdminConfig{
		Username: h.cfg.Admin.Username,
		Password: h.cfg.Admin.Password,
	}

	identity, err := auth.Login(r.Context(), h.store, adminCfg, username, password)
	if err != nil {
		middleware.AuditLog(r, "login_failed", username, "error", err.Error())
		pd := h.pageData(r, "Login")
		pd.Error = "invalid username or password"
		h.render(w, "login.html", http.StatusUnauthorized, pd)
		return
	}

	ttl := h.cfg.Server.SessionTTL
	if ttl == 0 {
		ttl = 12 * time.Hour
	}

	session, err := auth.CreateSessionForIdentity(r.Context(), h.store, identity, ttl)
	if err != nil {
		slog.Error("failed to create session", "error", err)
		pd := h.pageData(r, "Login")
		pd.Error = "could not create session, please try again"
		h.render(w, "login.html", http.StatusInternalServerError, pd)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     middleware.SessionCookieName,
		Value:    session.ID,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   h.isSecure(),
		Expires:  session.ExpiresAt,
	})

	middleware.AuditLog(r, "login_success", identity.Username)
	http.Redirect(w, r, "/", http.StatusFound)
}

// Logout handles POST /logout. It deletes the server-side session, clears the
// cookie, and redirects to /login.
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(middleware.SessionCookieName)
	if err == nil && cookie.Value != "" {
		if delErr := h.store.DeleteSession(r.Context(), cookie.Value); delErr != nil {
			slog.Warn("failed to delete session on logout", "error", delErr)
		}
	}

	// Clear the cookie by setting MaxAge=-1.
	http.SetCookie(w, &http.Cookie{
		Name:     middleware.SessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   h.isSecure(),
		MaxAge:   -1,
	})

	http.Redirect(w, r, "/login", http.StatusFound)
}

// ---------------------------------------------------------------------------
// Home
// ---------------------------------------------------------------------------

// homePageData is the template data for the home page.
type homePageData struct {
	RecentRequests []sqlite.RecentRequest
}

// Home handles GET /. Admins see all recent requests; regular users see only
// their own.
func (h *Handler) Home(w http.ResponseWriter, r *http.Request) {
	identity := middleware.MustGetIdentity(r.Context())

	const limit = 10

	var recentRequests []sqlite.RecentRequest
	var err error

	if identity.IsAdmin {
		recentRequests, err = h.store.ListAllRecentRequests(r.Context(), limit)
	} else {
		recentRequests, err = h.store.ListRecentRequestsByUser(r.Context(), identity.UserID, limit)
	}

	if err != nil {
		slog.Error("failed to list recent requests", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	pd := h.pageData(r, "Home")
	pd.Data = homePageData{RecentRequests: recentRequests}
	h.render(w, "home.html", http.StatusOK, pd)
}

// ---------------------------------------------------------------------------
// Sign handlers (user-facing)
// ---------------------------------------------------------------------------

// SignNew handles GET /sign/new and renders the new sign request form.
func (h *Handler) SignNew(w http.ResponseWriter, r *http.Request) {
	pd := h.pageData(r, "New Sign Request")
	h.render(w, "sign_new.html", http.StatusOK, pd)
}

// signDetailData is the template data for the sign detail page.
type signDetailData struct {
	Request      *model.SignRequest
	Files        []model.SignFile
	DownloadURLs map[string]string // file ID -> presigned URL
}

// SignDetail handles GET /sign/{id}. It fetches the request and its files,
// enforces ownership (admins can view all), generates presigned download URLs
// for each file, and renders sign_detail.html.
func (h *Handler) SignDetail(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	identity := middleware.MustGetIdentity(r.Context())

	req, files, err := h.signSvc.GetRequestWithFiles(r.Context(), id)
	if err != nil {
		slog.Warn("sign detail: request not found", "id", id, "error", err)
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	if !identity.IsAdmin && req.UserID != identity.UserID {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	downloadURLs := make(map[string]string, len(files))
	for _, f := range files {
		url, _, urlErr := h.signSvc.GetFileDownloadURL(r.Context(), f.ID)
		if urlErr != nil {
			slog.Warn("sign detail: could not generate download URL",
				"file_id", f.ID, "error", urlErr)
			continue
		}
		downloadURLs[f.ID] = url
	}

	pd := h.pageData(r, "Sign Request")
	pd.Data = signDetailData{
		Request:      req,
		Files:        files,
		DownloadURLs: downloadURLs,
	}
	h.render(w, "sign_detail.html", http.StatusOK, pd)
}

// ---------------------------------------------------------------------------
// Release handlers (user-facing)
// ---------------------------------------------------------------------------

// ReleaseNew handles GET /release/new and renders the new release request form.
func (h *Handler) ReleaseNew(w http.ResponseWriter, r *http.Request) {
	pd := h.pageData(r, "New Release Request")
	h.render(w, "release_new.html", http.StatusOK, pd)
}

// releaseDetailData is the template data for the release detail page.
type releaseDetailData struct {
	Request       *model.ReleaseRequest
	Files         []model.ReleaseFile
	Verifications []model.ReleaseVerification
}

// ReleaseDetail handles GET /release/{id}. It fetches the request, files and
// verifications, enforces ownership, and renders release_detail.html.
func (h *Handler) ReleaseDetail(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	identity := middleware.MustGetIdentity(r.Context())

	req, files, err := h.releaseSvc.GetRequestWithFiles(r.Context(), id)
	if err != nil {
		slog.Warn("release detail: request not found", "id", id, "error", err)
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	if !identity.IsAdmin && req.UserID != identity.UserID {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	verifications, err := h.releaseSvc.ListVerifications(r.Context(), id)
	if err != nil {
		slog.Error("release detail: failed to list verifications", "id", id, "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	pd := h.pageData(r, "Release Request")
	pd.Data = releaseDetailData{
		Request:       req,
		Files:         files,
		Verifications: verifications,
	}
	h.render(w, "release_detail.html", http.StatusOK, pd)
}

// ---------------------------------------------------------------------------
// Admin sign handlers
// ---------------------------------------------------------------------------

// adminSignDetailData is the template data for the admin sign detail page.
type adminSignDetailData struct {
	Request      *model.SignRequest
	Files        []model.SignFile
	DownloadURLs map[string]string // file ID -> presigned URL
}

// AdminSignDetail handles GET /admin/sign/{id}. Renders the admin view of a
// sign request with upload/download/fail action buttons.
func (h *Handler) AdminSignDetail(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	req, files, err := h.signSvc.GetRequestWithFiles(r.Context(), id)
	if err != nil {
		slog.Warn("admin sign detail: request not found", "id", id, "error", err)
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	downloadURLs := make(map[string]string, len(files))
	for _, f := range files {
		url, _, urlErr := h.signSvc.GetFileDownloadURL(r.Context(), f.ID)
		if urlErr != nil {
			slog.Warn("admin sign detail: could not generate download URL",
				"file_id", f.ID, "error", urlErr)
			continue
		}
		downloadURLs[f.ID] = url
	}

	pd := h.pageData(r, "Admin - Sign Request")
	pd.Data = adminSignDetailData{
		Request:      req,
		Files:        files,
		DownloadURLs: downloadURLs,
	}
	h.render(w, "admin_sign_detail.html", http.StatusOK, pd)
}

// ---------------------------------------------------------------------------
// Admin release handlers
// ---------------------------------------------------------------------------

// adminReleaseDetailData is the template data for the admin release detail page.
type adminReleaseDetailData struct {
	Request       *model.ReleaseRequest
	Files         []model.ReleaseFile
	Verifications []model.ReleaseVerification
}

// AdminReleaseDetail handles GET /admin/release/{id}. Renders the admin view
// of a release request with a "done" action button.
func (h *Handler) AdminReleaseDetail(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	req, files, err := h.releaseSvc.GetRequestWithFiles(r.Context(), id)
	if err != nil {
		slog.Warn("admin release detail: request not found", "id", id, "error", err)
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	verifications, err := h.releaseSvc.ListVerifications(r.Context(), id)
	if err != nil {
		slog.Error("admin release detail: failed to list verifications", "id", id, "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	pd := h.pageData(r, "Admin - Release Request")
	pd.Data = adminReleaseDetailData{
		Request:       req,
		Files:         files,
		Verifications: verifications,
	}
	h.render(w, "admin_release_detail.html", http.StatusOK, pd)
}

// ---------------------------------------------------------------------------
// Admin list handlers
// ---------------------------------------------------------------------------

// adminUsersData is the template data for the admin users page.
type adminUsersData struct {
	Users []model.User
}

// AdminUsers handles GET /admin/users and renders the admin user list.
func (h *Handler) AdminUsers(w http.ResponseWriter, r *http.Request) {
	users, err := h.store.ListUsers(r.Context())
	if err != nil {
		slog.Error("admin users: failed to list users", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	pd := h.pageData(r, "Admin - Users")
	pd.Data = adminUsersData{Users: users}
	h.render(w, "admin_users.html", http.StatusOK, pd)
}

// adminCLIKeysData is the template data for the admin CLI keys page.
type adminCLIKeysData struct {
	CLIKeys []model.CLIKey
}

// AdminCLIKeys handles GET /admin/cli-keys and renders all CLI keys including
// their plaintext key_value for the admin to copy.
func (h *Handler) AdminCLIKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := h.store.ListCLIKeys(r.Context())
	if err != nil {
		slog.Error("admin cli keys: failed to list keys", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	pd := h.pageData(r, "Admin - CLI Keys")
	pd.Data = adminCLIKeysData{CLIKeys: keys}
	h.render(w, "admin_cli_keys.html", http.StatusOK, pd)
}

// adminAllData is the template data for the admin all-requests page.
type adminAllData struct {
	SignRequests    []model.SignRequest
	ReleaseRequests []model.ReleaseRequest
}

// AdminAll handles GET /admin/all and renders all sign and release requests.
func (h *Handler) AdminAll(w http.ResponseWriter, r *http.Request) {
	const limit = 100

	signRequests, err := h.store.ListAllSignRequests(r.Context(), limit)
	if err != nil {
		slog.Error("admin all: failed to list sign requests", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	releaseRequests, err := h.store.ListAllReleaseRequests(r.Context(), limit)
	if err != nil {
		slog.Error("admin all: failed to list release requests", "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	pd := h.pageData(r, "Admin - All Requests")
	pd.Data = adminAllData{
		SignRequests:    signRequests,
		ReleaseRequests: releaseRequests,
	}
	h.render(w, "admin_all.html", http.StatusOK, pd)
}
