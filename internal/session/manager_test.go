package session

import (
	"context"
	"errors"
	"testing"
	"time"

	gatewayruntime "copilot-sdkapi/internal/runtime"
)

func TestManagerReusesAndExpiresSessions(t *testing.T) {
	base := time.Unix(100, 0)
	manager := NewManager(5 * time.Minute)
	manager.now = func() time.Time { return base }

	created := 0
	factory := func(context.Context) (gatewayruntime.Session, error) {
		created++
		return &fakeSession{id: "session"}, nil
	}

	lease, err := manager.Acquire(context.Background(), "tenant:session", Spec{Model: "gpt-4.1"}, factory)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}

	base = base.Add(2 * time.Minute)
	if err := manager.CleanupExpired(); err != nil {
		t.Fatalf("cleanup active session: %v", err)
	}
	if created != 1 {
		t.Fatalf("expected one session after active cleanup, got %d", created)
	}

	base = base.Add(10 * time.Minute)
	if err := manager.CleanupExpired(); err != nil {
		t.Fatalf("cleanup expired session: %v", err)
	}

	lease, err = manager.Acquire(context.Background(), "tenant:session", Spec{Model: "gpt-4.1"}, factory)
	if err != nil {
		t.Fatalf("re-acquire: %v", err)
	}
	if !lease.Created() {
		t.Fatalf("expected expired session to be recreated")
	}
	if created != 2 {
		t.Fatalf("expected second session to be created, got %d", created)
	}
}

func TestManagerRejectsSpecMismatch(t *testing.T) {
	manager := NewManager(5 * time.Minute)
	factory := func(context.Context) (gatewayruntime.Session, error) {
		return &fakeSession{id: "session"}, nil
	}

	lease, err := manager.Acquire(context.Background(), "tenant:session", Spec{Model: "gpt-4.1"}, factory)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer lease.Release()

	if _, err := manager.Acquire(context.Background(), "tenant:session", Spec{Model: "claude"}, factory); err != ErrSessionSpecMismatch {
		t.Fatalf("expected ErrSessionSpecMismatch, got %v", err)
	}
}

func TestDiscardRemovesPersistentSession(t *testing.T) {
	manager := NewManager(5 * time.Minute)
	factory := func(context.Context) (gatewayruntime.Session, error) {
		return &fakeSession{id: "session"}, nil
	}

	lease, err := manager.Acquire(context.Background(), "tenant:session", Spec{Model: "gpt-4.1"}, factory)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := lease.Discard(); err != nil {
		t.Fatalf("discard: %v", err)
	}

	if _, exists := manager.sessions["tenant:session"]; exists {
		t.Fatalf("expected discarded session to be removed from manager")
	}
}

func TestAcquireHonorsContextWhileWaitingForExistingSession(t *testing.T) {
	manager := NewManager(5 * time.Minute)
	created := 0
	factory := func(context.Context) (gatewayruntime.Session, error) {
		created++
		return &fakeSession{id: "session"}, nil
	}

	held, err := manager.Acquire(context.Background(), "tenant:session", Spec{Model: "gpt-4.1"}, factory)
	if err != nil {
		t.Fatalf("initial acquire: %v", err)
	}
	defer held.Release()

	waitCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err = manager.Acquire(waitCtx, "tenant:session", Spec{Model: "gpt-4.1"}, factory)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded while waiting for session, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 250*time.Millisecond {
		t.Fatalf("context-aware acquire took too long: %v", elapsed)
	}
	if created != 1 {
		t.Fatalf("expected no new session creation while waiting, got %d", created)
	}
	if _, exists := manager.sessions["tenant:session"]; !exists {
		t.Fatalf("expected waiting timeout to keep healthy session registered")
	}

	if err := held.Release(); err != nil {
		t.Fatalf("release held lease: %v", err)
	}

	reused, err := manager.Acquire(context.Background(), "tenant:session", Spec{Model: "gpt-4.1"}, factory)
	if err != nil {
		t.Fatalf("reacquire after timeout: %v", err)
	}
	defer reused.Release()
	if reused.Created() {
		t.Fatalf("expected healthy session to remain reusable after waiter timeout")
	}
}

func TestCloseContextHonorsTimeout(t *testing.T) {
	manager := NewManager(5 * time.Minute)
	factory := func(context.Context) (gatewayruntime.Session, error) {
		return &fakeSession{id: "session"}, nil
	}

	held, err := manager.Acquire(context.Background(), "tenant:session", Spec{Model: "gpt-4.1"}, factory)
	if err != nil {
		t.Fatalf("initial acquire: %v", err)
	}
	defer held.Release()

	closeCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	start := time.Now()
	err = manager.CloseContext(closeCtx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded from bounded close, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 250*time.Millisecond {
		t.Fatalf("bounded close took too long: %v", elapsed)
	}
}

func TestManagerEnforcesPerNamespaceLimit(t *testing.T) {
	manager := NewManagerWithLimits(5*time.Minute, Limits{
		MaxActiveSessions:     4,
		MaxActivePerNamespace: 1,
	})
	factory := func(context.Context) (gatewayruntime.Session, error) {
		return &fakeSession{id: "session"}, nil
	}

	first, err := manager.Acquire(context.Background(), "ns:key-1", Spec{Model: "gpt-4.1"}, factory)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer first.Release()

	if _, err := manager.Acquire(context.Background(), "ns:key-2", Spec{Model: "gpt-4.1"}, factory); !errors.Is(err, ErrTooManySessionsForNamespace) {
		t.Fatalf("expected per-namespace limit error, got %v", err)
	}
}

func TestManagerEnforcesGlobalLimit(t *testing.T) {
	manager := NewManagerWithLimits(5*time.Minute, Limits{
		MaxActiveSessions:     1,
		MaxActivePerNamespace: 2,
	})
	factory := func(context.Context) (gatewayruntime.Session, error) {
		return &fakeSession{id: "session"}, nil
	}

	first, err := manager.Acquire(context.Background(), "ns1:key-1", Spec{Model: "gpt-4.1"}, factory)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer first.Release()

	if _, err := manager.Acquire(context.Background(), "ns2:key-2", Spec{Model: "gpt-4.1"}, factory); !errors.Is(err, ErrTooManySessions) {
		t.Fatalf("expected global limit error, got %v", err)
	}
}

type fakeSession struct {
	id string
}

func (f *fakeSession) ID() string { return f.id }

func (f *fakeSession) Send(context.Context, gatewayruntime.MessageOptions, gatewayruntime.EventHandler) (gatewayruntime.Result, error) {
	return gatewayruntime.Result{SessionID: f.id, Content: "ok"}, nil
}

func (f *fakeSession) Close() error { return nil }
