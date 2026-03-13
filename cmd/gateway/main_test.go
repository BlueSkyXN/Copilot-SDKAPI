package main

import (
	"context"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"copilot-sdkapi/internal/config"
)

func TestParseLogLevel(t *testing.T) {
	if got := parseLogLevel("debug"); got != slog.LevelDebug {
		t.Fatalf("expected debug level, got %v", got)
	}
	if got := parseLogLevel("warn"); got != slog.LevelWarn {
		t.Fatalf("expected warn level, got %v", got)
	}
	if got := parseLogLevel("error"); got != slog.LevelError {
		t.Fatalf("expected error level, got %v", got)
	}
	if got := parseLogLevel("anything-else"); got != slog.LevelInfo {
		t.Fatalf("expected info fallback, got %v", got)
	}
}

func TestNewHTTPServerUsesRequestTimeouts(t *testing.T) {
	baseCtx := context.WithValue(context.Background(), "test-key", "value")
	server := newHTTPServer(baseCtx, config.Config{
		ListenAddr:     ":9999",
		RequestTimeout: 45 * time.Second,
	}, http.NewServeMux())

	if server.Addr != ":9999" {
		t.Fatalf("unexpected listen addr %q", server.Addr)
	}
	if server.ReadHeaderTimeout != 10*time.Second {
		t.Fatalf("unexpected read header timeout %s", server.ReadHeaderTimeout)
	}
	if server.ReadTimeout != 45*time.Second {
		t.Fatalf("unexpected read timeout %s", server.ReadTimeout)
	}
	if server.IdleTimeout != 45*time.Second {
		t.Fatalf("unexpected idle timeout %s", server.IdleTimeout)
	}
	if got := server.BaseContext(nil); got != baseCtx {
		t.Fatalf("expected base context to be propagated")
	}
}

func TestNewHTTPServerAppliesMinimumTimeouts(t *testing.T) {
	server := newHTTPServer(context.Background(), config.Config{
		ListenAddr:     ":9999",
		RequestTimeout: 5 * time.Second,
	}, http.NewServeMux())

	if server.ReadTimeout != 10*time.Second {
		t.Fatalf("unexpected minimum read timeout %s", server.ReadTimeout)
	}
	if server.IdleTimeout != 30*time.Second {
		t.Fatalf("unexpected minimum idle timeout %s", server.IdleTimeout)
	}
}

func TestCloseWithContextReturnsCloserError(t *testing.T) {
	expected := context.Canceled
	err := closeWithContext(context.Background(), func() error {
		return expected
	})
	if err != expected {
		t.Fatalf("expected closer error %v, got %v", expected, err)
	}
}

func TestCloseWithContextHonorsDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	err := closeWithContext(ctx, func() error {
		<-time.After(200 * time.Millisecond)
		return nil
	})
	if err != context.DeadlineExceeded {
		t.Fatalf("expected deadline exceeded, got %v", err)
	}
}
