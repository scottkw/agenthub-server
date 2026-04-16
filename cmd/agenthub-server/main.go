// Command agenthub-server boots the server: config → logger → db →
// migrations → supervisor → HTTP frontend.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/go-chi/chi/v5"

	"github.com/scottkw/agenthub-server/internal/api"
	"github.com/scottkw/agenthub-server/internal/auth"
	"github.com/scottkw/agenthub-server/internal/config"
	dbpkg "github.com/scottkw/agenthub-server/internal/db"
	"github.com/scottkw/agenthub-server/internal/db/migrations"
	"github.com/scottkw/agenthub-server/internal/db/sqlite"
	"github.com/scottkw/agenthub-server/internal/httpfront"
	"github.com/scottkw/agenthub-server/internal/mail"
	"github.com/scottkw/agenthub-server/internal/obs"
	"github.com/scottkw/agenthub-server/internal/supervisor"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "agenthub-server:", err)
		os.Exit(1)
	}
}

func run() error {
	var cfgPath string
	flag.StringVar(&cfgPath, "config", "", "path to config.yaml")
	flag.Parse()

	cfg, err := config.Load(config.LoadOptions{ConfigPath: cfgPath})
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return err
	}

	logger := obs.NewLogger(obs.Options{
		Format: obs.Format(cfg.Obs.LogFormat),
		Level:  parseLevel(cfg.Obs.LogLevel),
	})
	logger.Info("boot", "mode", cfg.Mode, "version", version)

	db, err := openDB(cfg)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer db.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := migrations.Apply(ctx, db); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	logger.Info("migrations applied", "driver", db.Driver())

	authSvc, err := buildAuthService(ctx, cfg, db, logger)
	if err != nil {
		return fmt.Errorf("build auth service: %w", err)
	}

	router := chi.NewRouter()
	router.Mount("/healthz", api.NewHealthHandler(db, version))
	router.Mount("/api/auth", api.AuthRoutes(authSvc))

	front, err := newFrontend(cfg, router)
	if err != nil {
		return fmt.Errorf("http frontend: %w", err)
	}

	err = supervisor.Run(ctx, []supervisor.Service{
		{
			Name:  "httpfront",
			Start: front.Start,
		},
	})
	if err != nil && err != context.Canceled {
		return err
	}
	logger.Info("shutdown complete")
	return nil
}

func openDB(cfg config.Config) (dbpkg.DB, error) {
	switch cfg.DB.Driver {
	case config.DriverSQLite:
		if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
			return nil, fmt.Errorf("mkdir data_dir: %w", err)
		}
		return sqlite.Open(sqlite.Options{Path: filepath.Join(cfg.DataDir, "agenthub.db")})
	default:
		return nil, fmt.Errorf("unsupported db driver %q (postgres lands in Plan 08)", cfg.DB.Driver)
	}
}

func newFrontend(cfg config.Config, h http.Handler) (*httpfront.Server, error) {
	switch cfg.TLS.Mode {
	case config.TLSModeOff:
		return httpfront.New(httpfront.Options{
			Mode:    httpfront.ModePlain,
			Address: fmt.Sprintf("0.0.0.0:%d", cfg.HTTP.Port),
			Handler: h,
		})
	case config.TLSModeFile:
		return httpfront.New(httpfront.Options{
			Mode:     httpfront.ModeFile,
			Address:  fmt.Sprintf("0.0.0.0:%d", cfg.HTTP.Port),
			Handler:  h,
			CertFile: cfg.TLS.CertFile,
			KeyFile:  cfg.TLS.KeyFile,
		})
	case config.TLSModeAuto:
		return httpfront.New(httpfront.Options{
			Mode:    httpfront.ModeAuto,
			Handler: h,
			Email:   cfg.TLS.Email,
			Domains: []string{cfg.Hostname},
		})
	}
	return nil, fmt.Errorf("unknown tls.mode %q", cfg.TLS.Mode)
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func buildAuthService(ctx context.Context, cfg config.Config, db dbpkg.DB, log *slog.Logger) (*auth.Service, error) {
	key, err := auth.LoadOrCreateJWTKey(ctx, db.SQL())
	if err != nil {
		return nil, err
	}
	signer := auth.NewJWTSigner(key, cfg.Auth.Issuer)

	var mailer mail.Mailer
	switch cfg.Mail.Provider {
	case "smtp":
		mailer = mail.NewSMTP(mail.SMTPConfig{
			Host:     cfg.Mail.SMTP.Host,
			Port:     cfg.Mail.SMTP.Port,
			Username: cfg.Mail.SMTP.Username,
			Password: cfg.Mail.SMTP.Password,
			From:     cfg.Mail.From,
		})
	default:
		mailer = mail.NewNoop(log)
	}

	return auth.NewService(auth.Config{
		DB:              db.SQL(),
		Signer:          signer,
		Mailer:          mailer,
		Log:             log,
		TTL:             auth.Lifetimes{Session: cfg.Auth.SessionTTL, EmailVerify: cfg.Auth.EmailVerifyTTL, PasswordReset: cfg.Auth.PasswordResetTTL},
		From:            cfg.Mail.From,
		VerifyURLPrefix: cfg.Auth.VerifyURLPrefix,
		ResetURLPrefix:  cfg.Auth.ResetURLPrefix,
	}), nil
}
