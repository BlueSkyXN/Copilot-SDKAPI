package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"copilot-sdkapi/internal/auth"
	"copilot-sdkapi/internal/config"
	_ "copilot-sdkapi/internal/embeddedcli"
	"copilot-sdkapi/internal/httpapi"
	gatewayruntime "copilot-sdkapi/internal/runtime"
	"copilot-sdkapi/internal/session"
	"copilot-sdkapi/internal/usage"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		panic(err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: parseLogLevel(cfg.LogLevel),
	}))
	if cfg.UsingDefaultAPIKey && len(cfg.APIKeys) > 0 {
		logger.Warn("GATEWAY_API_KEYS is unset; using local trial API key", slog.String("api_key", cfg.APIKeys[0].Secret))
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	provider := gatewayruntime.NewCopilotProvider(gatewayruntime.Options{
		CLIPath:          cfg.CLIPath,
		GitHubToken:      cfg.GitHubToken,
		UseLoggedInUser:  cfg.UseLoggedInUser,
		LogLevel:         cfg.LogLevel,
		ClientName:       cfg.ClientName,
		WorkingDirectory: cfg.WorkingDirectory,
		ConfigDir:        cfg.ConfigDir,
		DefaultModel:     cfg.DefaultModel,
		AvailableTools:   cfg.SDKAvailableTools,
		ExcludedTools:    cfg.SDKExcludedTools,
		SkillDirectories: cfg.SDKSkillDirs,
		DisabledSkills:   cfg.SDKDisabledSkills,
		MCPServers:       cfg.SDKMCPServers,
		CustomAgents:     cfg.SDKCustomAgents,
		DefaultAgent:     cfg.SDKDefaultAgent,
		PermissionMode:   cfg.SDKPermissionMode,
		InfiniteSessions: cfg.SDKInfiniteSessions,
	})
	if err := provider.Start(ctx); err != nil {
		logger.Error("failed to start copilot runtime", slog.Any("err", err))
		os.Exit(1)
	}

	authStore := auth.NewStore(cfg.APIKeys)
	sessionStore := session.NewFileStore(cfg.SessionStorePath)
	sessionManager := session.NewManagerWithStore(cfg.SessionTTL, session.Limits{
		MaxActiveSessions:     cfg.MaxActiveSessions,
		MaxActivePerNamespace: cfg.MaxSessionsPerKey,
	}, sessionStore)
	recorder := usage.NewRecorder(logger)
	server := httpapi.NewServer(cfg, authStore, provider, sessionManager, recorder, logger)

	httpServer := newHTTPServer(ctx, cfg, server.Handler())

	go runSessionCleanup(ctx, logger, sessionManager, cfg.SessionTTL)

	go func() {
		logger.Info("starting copilot-sdkapi", slog.String("addr", cfg.ListenAddr), slog.String("default_model", cfg.DefaultModel))
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("copilot-sdkapi server stopped unexpectedly", slog.Any("err", err))
			stop()
		}
	}()

	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("http shutdown failed", slog.Any("err", err))
	}
	if err := sessionManager.DisconnectContext(shutdownCtx); err != nil {
		logger.Error("session shutdown failed", slog.Any("err", err))
	}
	if err := closeWithContext(shutdownCtx, provider.Close); err != nil {
		logger.Error("runtime shutdown failed", slog.Any("err", err))
	}
}

func runSessionCleanup(ctx context.Context, logger *slog.Logger, manager *session.Manager, ttl time.Duration) {
	if ttl <= 0 {
		return
	}
	interval := ttl / 2
	if interval < time.Minute {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := manager.CleanupExpired(); err != nil {
				logger.Warn("session cleanup failed", slog.Any("err", err))
			}
		}
	}
}

func newHTTPServer(baseCtx context.Context, cfg config.Config, handler http.Handler) *http.Server {
	requestTimeout := cfg.RequestTimeout
	if requestTimeout < 10*time.Second {
		requestTimeout = 10 * time.Second
	}

	idleTimeout := cfg.RequestTimeout
	if idleTimeout < 30*time.Second {
		idleTimeout = 30 * time.Second
	}

	return &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       requestTimeout,
		IdleTimeout:       idleTimeout,
		BaseContext: func(net.Listener) context.Context {
			return baseCtx
		},
	}
}

func closeWithContext(ctx context.Context, closer func() error) error {
	resultCh := make(chan error, 1)
	go func() {
		resultCh <- closer()
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-resultCh:
		return err
	}
}

func parseLogLevel(level string) slog.Level {
	switch level {
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
