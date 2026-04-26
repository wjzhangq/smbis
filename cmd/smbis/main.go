package main

import (
	"context"
	"flag"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"math"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/wj/smbis/internal/config"
	"github.com/wj/smbis/internal/handler/api"
	"github.com/wj/smbis/internal/handler/cli"
	webhandler "github.com/wj/smbis/internal/handler/web"
	"github.com/wj/smbis/internal/middleware"
	"github.com/wj/smbis/internal/service/release"
	"github.com/wj/smbis/internal/service/sign"
	"github.com/wj/smbis/internal/store/oss"
	"github.com/wj/smbis/internal/store/sqlite"
	webfs "github.com/wj/smbis/web"
)

// Build-time variables injected via ldflags.
var (
	Version   = "dev"
	Commit    = "unknown"
	BuildTime = "unknown"
)

func main() {
	cfgPath := flag.String("config", "config.yaml", "path to configuration file")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("smbis %s (commit: %s, built: %s)\n", Version, Commit, BuildTime)
		os.Exit(0)
	}

	// Load configuration.
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}

	// Setup structured logging with JSON handler to stdout.
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	// Init SQLite store.
	store, err := sqlite.New(cfg.Storage.SQLitePath)
	if err != nil {
		slog.Error("failed to init sqlite store", "error", err)
		os.Exit(1)
	}
	defer store.Close()

	// Init OSS client.
	ossClient, err := oss.New(oss.Config{
		Endpoint:         cfg.OSS.Endpoint,
		InternalEndpoint: cfg.OSS.InternalEndpoint,
		AccessKeyID:      cfg.OSS.AccessKeyID,
		AccessKeySecret:  cfg.OSS.AccessKeySecret,
		Bucket:           cfg.OSS.Bucket,
		Prefix:           cfg.OSS.Prefix,
		PresignTTL:       cfg.OSS.PresignTTL,
	})
	if err != nil {
		slog.Error("failed to init oss client", "error", err)
		os.Exit(1)
	}

	// Init sign service.
	signSvc := sign.New(
		store,
		ossClient,
		int64(cfg.Server.UploadChunkSize),
		int64(cfg.Server.MaxFileSize),
	)

	// Init release service.
	releaseSvc := release.New(
		store,
		ossClient,
		int64(cfg.Server.UploadChunkSize),
		int64(cfg.Server.MaxFileSize),
		cfg.Verify.HTTPTimeout,
		cfg.Verify.FollowRedirects,
	)

	// Build template FuncMap with helper functions.
	funcMap := template.FuncMap{
		"formatSize": func(bytes int64) string {
			const (
				kb = 1024
				mb = 1024 * kb
				gb = 1024 * mb
			)
			switch {
			case bytes >= gb:
				return fmt.Sprintf("%.2f GB", float64(bytes)/float64(gb))
			case bytes >= mb:
				return fmt.Sprintf("%.2f MB", float64(bytes)/float64(mb))
			case bytes >= kb:
				return fmt.Sprintf("%.2f KB", float64(bytes)/float64(kb))
			default:
				return fmt.Sprintf("%d B", bytes)
			}
		},
		"formatTime": func(t time.Time) string {
			return t.Format("2006-01-02 15:04")
		},
		"timeAgo": func(t time.Time) string {
			diff := time.Since(t)
			minutes := diff.Minutes()
			hours := diff.Hours()
			days := hours / 24

			switch {
			case minutes < 1:
				return "just now"
			case minutes < 60:
				n := int(math.Round(minutes))
				return fmt.Sprintf("%dm ago", n)
			case hours < 24:
				n := int(math.Round(hours))
				return fmt.Sprintf("%dh ago", n)
			case days < 30:
				n := int(math.Round(days))
				return fmt.Sprintf("%dd ago", n)
			case days < 365:
				n := int(math.Round(days / 30))
				return fmt.Sprintf("%dmo ago", n)
			default:
				n := int(math.Round(days / 365))
				return fmt.Sprintf("%dy ago", n)
			}
		},
		"statusColor": func(status string) string {
			switch status {
			case "pending":
				return "yellow"
			case "done", "signed":
				return "green"
			case "canceled":
				return "gray"
			default:
				return "gray"
			}
		},
		"add": func(a, b int) int {
			return a + b
		},
		"upper": func(s string) string {
			return strings.ToUpper(s)
		},
		"deref": func(v any) any {
			switch p := v.(type) {
			case *string:
				if p == nil {
					return ""
				}
				return *p
			case *int:
				if p == nil {
					return 0
				}
				return *p
			case *int64:
				if p == nil {
					return int64(0)
				}
				return *p
			case *time.Time:
				if p == nil {
					return time.Time{}
				}
				return *p
			default:
				return v
			}
		},
		"index": func(m map[string]string, key string) string {
			return m[key]
		},
	}

	// Parse templates from embedded FS.
	tmpl, err := template.New("").Funcs(funcMap).ParseFS(webfs.TemplateFS, "templates/*.html")
	if err != nil {
		slog.Error("failed to parse templates", "error", err)
		os.Exit(1)
	}

	// Init handlers.
	webH := webhandler.New(store, signSvc, releaseSvc, cfg, tmpl)
	apiH := api.New(store, signSvc, releaseSvc)
	cliH := cli.New(signSvc, cfg.Server.ExternalURL)

	// Get sub filesystem for static files.
	staticSubFS, err := fs.Sub(webfs.StaticFS, "static")
	if err != nil {
		slog.Error("failed to create static sub fs", "error", err)
		os.Exit(1)
	}

	// Setup chi router.
	r := chi.NewRouter()
	r.Use(middleware.Recover)
	r.Use(middleware.Logger)
	r.Use(chimw.RealIP)

	// Static files.
	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServerFS(staticSubFS)))

	// Public routes.
	r.Get("/login", webH.LoginPage)
	r.Post("/login", webH.Login)

	// Authenticated routes.
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireAuth(store))

		r.Post("/logout", webH.Logout)
		r.Get("/", webH.Home)

		// Sign request routes.
		r.Get("/sign/new", webH.SignNew)
		r.Get("/sign/{id}", webH.SignDetail)

		// Release request routes.
		r.Get("/release/new", webH.ReleaseNew)
		r.Get("/release/{id}", webH.ReleaseDetail)

		// Browser API routes.
		r.Route("/api", func(r chi.Router) {
			r.Post("/sign/requests", apiH.CreateSignRequest)
			r.Post("/sign/requests/{id}/files/init", apiH.InitSignFileUpload)
			r.Post("/sign/requests/{id}/files/{draftId}/part", apiH.UploadSignFilePart)
			r.Post("/sign/requests/{id}/files/{draftId}/complete", apiH.CompleteSignFileUpload)
			r.Delete("/sign/requests/{id}/files/{draftId}", apiH.AbortSignFileUpload)

			r.Post("/release/requests", apiH.CreateReleaseRequest)
			r.Post("/release/requests/{id}/files/init", apiH.InitReleaseFileUpload)
			r.Post("/release/requests/{id}/files/{draftId}/part", apiH.UploadReleaseFilePart)
			r.Post("/release/requests/{id}/files/{draftId}/complete", apiH.CompleteReleaseFileUpload)
			r.Delete("/release/requests/{id}/files/{draftId}", apiH.AbortReleaseFileUpload)
			r.Post("/release/requests/{id}/verify", apiH.VerifyReleaseURL)

			// Admin API routes.
			r.Route("/admin", func(r chi.Router) {
				r.Use(middleware.RequireAdmin)

				r.Post("/sign/files/{fileId}/signed-upload/init", apiH.AdminInitSignedUpload)
				r.Post("/sign/files/{fileId}/signed-upload/{draftId}/part", apiH.AdminUploadSignedPart)
				r.Post("/sign/files/{fileId}/signed-upload/{draftId}/complete", apiH.AdminCompleteSignedUpload)
				r.Post("/sign/files/{fileId}/fail", apiH.AdminMarkSignFileFailed)

				r.Post("/release/requests/{id}/done", apiH.AdminMarkReleaseDone)

				r.Post("/users", apiH.CreateUser)
				r.Post("/users/{id}/reset-password", apiH.ResetUserPassword)
				r.Post("/users/{id}/disable", apiH.DisableUser)

				r.Get("/cli-keys", apiH.ListCLIKeys)
				r.Post("/cli-keys", apiH.CreateCLIKey)
				r.Delete("/cli-keys/{id}", apiH.RevokeCLIKey)
			})
		})

		// Admin web routes.
		r.Route("/admin", func(r chi.Router) {
			r.Use(middleware.RequireAdmin)
			r.Get("/users", webH.AdminUsers)
			r.Get("/cli-keys", webH.AdminCLIKeys)
			r.Get("/sign/{id}", webH.AdminSignDetail)
			r.Get("/release/{id}", webH.AdminReleaseDetail)
			r.Get("/all", webH.AdminAll)
		})
	})

	// CLI routes (no browser session auth).
	r.Route("/cli", func(r chi.Router) {
		r.Get("/sign/{requestId}/files", cliH.ListFiles)
		r.Get("/sign/{requestId}/files/{fileId}/download", cliH.DownloadFile)

		// CLI routes requiring CLI Key auth.
		r.Group(func(r chi.Router) {
			r.Use(middleware.CLIAuth(store))
			r.Post("/sign/{requestId}/files/{fileId}/signed", cliH.UploadSignedFile)
			r.Post("/sign/{requestId}/files/{fileId}/upload/init", cliH.InitUpload)
			r.Post("/sign/{requestId}/files/{fileId}/upload/{draftId}/part", cliH.UploadPart)
			r.Post("/sign/{requestId}/files/{fileId}/upload/{draftId}/complete", cliH.CompleteUpload)
			r.Post("/sign/{requestId}/files/{fileId}/fail", cliH.MarkFileFailed)
		})
	})

	// Background cleanup goroutine.
	cleanupCtx, cleanupCancel := context.WithCancel(context.Background())
	defer cleanupCancel()

	go func() {
		sessionTicker := time.NewTicker(10 * time.Minute)
		draftTicker := time.NewTicker(24 * time.Hour)
		defer sessionTicker.Stop()
		defer draftTicker.Stop()

		for {
			select {
			case <-cleanupCtx.Done():
				return

			case <-sessionTicker.C:
				n, cleanErr := store.CleanExpiredSessions(cleanupCtx)
				if cleanErr != nil {
					slog.Error("cleanup: failed to clean expired sessions", "error", cleanErr)
				} else if n > 0 {
					slog.Info("cleanup: removed expired sessions", "count", n)
				}

			case <-draftTicker.C:
				drafts, listErr := store.ListExpiredUploadDrafts(cleanupCtx)
				if listErr != nil {
					slog.Error("cleanup: failed to list expired upload drafts", "error", listErr)
					continue
				}
				for _, d := range drafts {
					if abortErr := ossClient.AbortMultipartUpload(cleanupCtx, d.OSSKey, d.OSSUploadID); abortErr != nil {
						slog.Error("cleanup: failed to abort expired oss multipart upload",
							"draft_id", d.ID,
							"oss_key", d.OSSKey,
							"upload_id", d.OSSUploadID,
							"error", abortErr,
						)
					}
					if delErr := store.DeleteUploadDraft(cleanupCtx, d.ID); delErr != nil {
						slog.Error("cleanup: failed to delete expired upload draft",
							"draft_id", d.ID,
							"error", delErr,
						)
					}
				}
				if len(drafts) > 0 {
					slog.Info("cleanup: processed expired upload drafts", "count", len(drafts))
				}
			}
		}
	}()

	// Build HTTP server.
	srv := &http.Server{
		Addr:         cfg.Server.Listen,
		Handler:      r,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 5 * time.Minute,
		IdleTimeout:  120 * time.Second,
	}

	// Start server in a goroutine so we can listen for shutdown signals.
	serverErr := make(chan error, 1)
	go func() {
		slog.Info("server starting", "addr", cfg.Server.Listen, "version", Version, "commit", Commit)
		if listenErr := srv.ListenAndServe(); listenErr != nil && listenErr != http.ErrServerClosed {
			serverErr <- listenErr
		}
		close(serverErr)
	}()

	// Wait for interrupt/termination signal or server error.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-quit:
		slog.Info("shutdown signal received", "signal", sig)
	case srvErr := <-serverErr:
		if srvErr != nil {
			slog.Error("server error", "error", srvErr)
			os.Exit(1)
		}
	}

	// Stop cleanup goroutine.
	cleanupCancel()

	// Graceful shutdown with timeout.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	slog.Info("shutting down server gracefully")
	if shutErr := srv.Shutdown(shutdownCtx); shutErr != nil {
		slog.Error("server shutdown error", "error", shutErr)
		os.Exit(1)
	}

	slog.Info("server stopped")
}
