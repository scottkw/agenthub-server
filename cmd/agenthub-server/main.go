// Command agenthub-server boots the server: config → logger → db →
// migrations → supervisor → HTTP frontend.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/go-chi/chi/v5"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/github"
	"golang.org/x/oauth2/google"

	"github.com/scottkw/agenthub-server/internal/api"
	"github.com/scottkw/agenthub-server/internal/auth"
	"github.com/scottkw/agenthub-server/internal/config"
	dbpkg "github.com/scottkw/agenthub-server/internal/db"
	"github.com/scottkw/agenthub-server/internal/db/migrations"
	"github.com/scottkw/agenthub-server/internal/db/sqlite"
	"github.com/scottkw/agenthub-server/internal/devices"
	"github.com/scottkw/agenthub-server/internal/headscale"
	"github.com/scottkw/agenthub-server/internal/httpfront"
	"github.com/scottkw/agenthub-server/internal/httpmw"
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

	headscaler, hsSupervisor, hsClient, err := buildHeadscaler(ctx, cfg, db, logger)
	if err != nil {
		return fmt.Errorf("headscale: %w", err)
	}
	if hsClient != nil {
		defer hsClient.Close()
	}

	router := chi.NewRouter()
	router.Mount("/healthz", api.NewHealthHandler(db, version))

	rl := httpmw.NewRateLimit(httpmw.RateLimitConfig{
		RequestsPerSecond: cfg.RateLimit.RequestsPerSecond,
		Burst:             cfg.RateLimit.Burst,
	})
	idem := httpmw.NewIdempotency(httpmw.IdempotencyConfig{DB: db.SQL()})
	router.With(rl, idem).Mount("/api/auth", api.AuthRoutes(authSvc))

	var wirings []api.OAuthProviderWiring
	if cfg.OAuth.Google.ClientID != "" {
		wirings = append(wirings, api.OAuthProviderWiring{
			Provider: auth.OAuthProviderGoogle,
			OAuth2: &oauth2.Config{
				ClientID:     cfg.OAuth.Google.ClientID,
				ClientSecret: cfg.OAuth.Google.ClientSecret,
				RedirectURL:  cfg.OAuth.Google.RedirectURL,
				Scopes:       []string{"openid", "email", "profile"},
				Endpoint:     google.Endpoint,
			},
			UserInfoURL: "https://openidconnect.googleapis.com/v1/userinfo",
		})
	}
	if cfg.OAuth.GitHub.ClientID != "" {
		wirings = append(wirings, api.OAuthProviderWiring{
			Provider: auth.OAuthProviderGitHub,
			OAuth2: &oauth2.Config{
				ClientID:     cfg.OAuth.GitHub.ClientID,
				ClientSecret: cfg.OAuth.GitHub.ClientSecret,
				RedirectURL:  cfg.OAuth.GitHub.RedirectURL,
				Scopes:       []string{"user:email"},
				Endpoint:     github.Endpoint,
			},
			UserInfoURL: "https://api.github.com/user",
		})
	}
	if len(wirings) > 0 {
		router.With(rl).Mount("/api/auth/oauth", api.OAuthRoutes(authSvc, wirings))
	}

	router.Mount("/api/tokens", api.APITokenRoutes(authSvc))

	// /api/devices: claim is unauthenticated (pair code authenticates) but
	// is rate-limited to slow brute-force of the code. Other endpoints use
	// the router's own auth middleware.
	router.With(rl).Mount("/api/devices", api.DeviceRoutes(authSvc, headscaler, nil))

	// /api/sessions: all endpoints are authed; no rate-limit (machine traffic).
	router.Mount("/api/sessions", api.SessionRoutes(authSvc))

	if cfg.Headscale.Enabled {
		hsURL, err := url.Parse("http://" + cfg.Headscale.ListenAddr)
		if err != nil {
			return fmt.Errorf("parse headscale listen_addr: %w", err)
		}
		proxy := httputil.NewSingleHostReverseProxy(hsURL)
		router.Mount("/headscale", http.StripPrefix("/headscale", proxy))

		if cfg.Headscale.DERPEnabled {
			derpProxy := httputil.NewSingleHostReverseProxy(hsURL)
			router.Mount("/derp", derpProxy)
		}
	}

	front, err := newFrontend(cfg, router)
	if err != nil {
		return fmt.Errorf("http frontend: %w", err)
	}

	services := []supervisor.Service{
		{Name: "httpfront", Start: front.Start},
	}
	if hsSupervisor != nil {
		services = append(services, supervisor.Service{
			Name: "headscale",
			Start: func(ctx context.Context) error {
				return hsSupervisor.Wait(ctx)
			},
		})
	}

	err = supervisor.Run(ctx, services)
	if err != nil && err != context.Canceled {
		return err
	}
	logger.Info("shutdown complete")
	return nil
}

// buildHeadscaler returns the Headscaler implementation the api package
// should use. When Headscale is disabled (cfg.Headscale.Enabled=false)
// we keep the StubHeadscaler and return nil for the supervisor/client.
// When enabled we start the subprocess, wait for /health, dial the
// gRPC UNIX socket, and return a *headscale.Service.
func buildHeadscaler(ctx context.Context, cfg config.Config, db dbpkg.DB, log *slog.Logger) (devices.Headscaler, *headscale.Supervisor, *headscale.Client, error) {
	if !cfg.Headscale.Enabled {
		log.Info("headscale disabled (StubHeadscaler in use)")
		return devices.StubHeadscaler{}, nil, nil, nil
	}

	opts := headscale.Options{
		BinaryPath:         cfg.Headscale.BinaryPath,
		DataDir:            cfg.Headscale.DataDir,
		ServerURL:          cfg.Headscale.ServerURL,
		ListenAddr:         cfg.Headscale.ListenAddr,
		GRPCListenAddr:     cfg.Headscale.GRPCListenAddr,
		UnixSocket:         cfg.Headscale.UnixSocket,
		ShutdownTimeout:    cfg.Headscale.ShutdownTimeout,
		ReadyTimeout:       cfg.Headscale.ReadyTimeout,
		DERPEnabled:        cfg.Headscale.DERPEnabled,
		DERPRegionID:       cfg.Headscale.DERPRegionID,
		DERPRegionCode:     cfg.Headscale.DERPRegionCode,
		DERPRegionName:     cfg.Headscale.DERPRegionName,
		DERPSTUNListenAddr: cfg.Headscale.DERPSTUNListenAddr,
		DERPVerifyClients:  cfg.Headscale.DERPVerifyClients,
		DERPIPv4:           cfg.Headscale.DERPIPv4,
		DERPIPv6:           cfg.Headscale.DERPIPv6,
	}
	sv := headscale.NewSupervisor(opts, "http://"+cfg.Headscale.ListenAddr).WithLogger(log)
	if err := sv.Start(ctx); err != nil {
		return nil, nil, nil, fmt.Errorf("start supervisor: %w", err)
	}

	client, err := headscale.Dial(cfg.Headscale.UnixSocket)
	if err != nil {
		_ = sv.Wait(ctx) // best-effort
		return nil, nil, nil, fmt.Errorf("dial grpc: %w", err)
	}

	var derpMapJSON string
	if cfg.Headscale.DERPEnabled {
		raw, err := headscale.BuildDERPMap(headscale.DERPMapInput{
			RegionID:   cfg.Headscale.DERPRegionID,
			RegionCode: cfg.Headscale.DERPRegionCode,
			RegionName: cfg.Headscale.DERPRegionName,
			Hostname:   cfg.Headscale.DERPHostname,
			DERPPort:   cfg.Headscale.DERPPort,
			STUNPort:   3478,
			IPv4:       cfg.Headscale.DERPIPv4,
			IPv6:       cfg.Headscale.DERPIPv6,
		})
		if err != nil {
			_ = client.Close()
			_ = sv.Wait(ctx)
			return nil, nil, nil, fmt.Errorf("build derp map: %w", err)
		}
		derpMapJSON = string(raw)
	}

	svc := &headscale.Service{
		DB:          db.SQL(),
		Client:      client,
		ServerURL:   cfg.Headscale.ServerURL,
		DERPMapJSON: derpMapJSON,
		UserPrefix:  "u-",
	}
	return svc, sv, client, nil
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
