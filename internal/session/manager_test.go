package session

import (
	"context"
	"errors"
	"sync/atomic"
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

	lease, err := manager.Acquire(context.Background(), "tenant:session", Spec{Model: "gpt-4.1"}, factory, nil, nil)
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

	lease, err = manager.Acquire(context.Background(), "tenant:session", Spec{Model: "gpt-4.1"}, factory, nil, nil)
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

	lease, err := manager.Acquire(context.Background(), "tenant:session", Spec{Model: "gpt-4.1"}, factory, nil, nil)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer lease.Release()

	if _, err := manager.Acquire(context.Background(), "tenant:session", Spec{Model: "claude"}, factory, nil, nil); err != ErrSessionSpecMismatch {
		t.Fatalf("expected ErrSessionSpecMismatch, got %v", err)
	}
}

func TestDiscardRemovesPersistentSession(t *testing.T) {
	manager := NewManager(5 * time.Minute)
	factory := func(context.Context) (gatewayruntime.Session, error) {
		return &fakeSession{id: "session"}, nil
	}

	lease, err := manager.Acquire(context.Background(), "tenant:session", Spec{Model: "gpt-4.1"}, factory, nil, nil)
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

	held, err := manager.Acquire(context.Background(), "tenant:session", Spec{Model: "gpt-4.1"}, factory, nil, nil)
	if err != nil {
		t.Fatalf("initial acquire: %v", err)
	}
	defer held.Release()

	waitCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err = manager.Acquire(waitCtx, "tenant:session", Spec{Model: "gpt-4.1"}, factory, nil, nil)
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

	reused, err := manager.Acquire(context.Background(), "tenant:session", Spec{Model: "gpt-4.1"}, factory, nil, nil)
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

	held, err := manager.Acquire(context.Background(), "tenant:session", Spec{Model: "gpt-4.1"}, factory, nil, nil)
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

func TestDisconnectContextPreservesPersistentSession(t *testing.T) {
	store := NewFileStore(t.TempDir() + "/sessions.json")
	manager := NewManagerWithStore(5*time.Minute, Limits{}, store)

	created := 0
	factory := func(context.Context) (gatewayruntime.Session, error) {
		created++
		return &fakeSession{id: "tenant:session"}, nil
	}
	deleted := 0
	deleteByID := func(context.Context, string) error {
		deleted++
		return nil
	}

	lease, err := manager.Acquire(context.Background(), "tenant:session", Spec{Model: "gpt-4.1"}, factory, nil, deleteByID)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := manager.DisconnectContext(context.Background()); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	if deleted != 0 {
		t.Fatalf("expected graceful disconnect to preserve upstream session, got %d deletions", deleted)
	}
	record, err := store.Load("tenant:session")
	if err != nil {
		t.Fatalf("load store: %v", err)
	}
	if record == nil || record.SessionID != "tenant:session" {
		t.Fatalf("expected persistent record to remain after graceful disconnect, got %#v", record)
	}

	resumed := 0
	restarted := NewManagerWithStore(5*time.Minute, Limits{}, store)
	resume := func(_ context.Context, sessionID string) (gatewayruntime.Session, error) {
		resumed++
		return &fakeSession{id: sessionID}, nil
	}
	reused, err := restarted.Acquire(context.Background(), "tenant:session", Spec{Model: "gpt-4.1"}, factory, resume, deleteByID)
	if err != nil {
		t.Fatalf("resume after disconnect: %v", err)
	}
	defer reused.Release()
	if reused.Created() {
		t.Fatalf("expected graceful disconnect to preserve resumable session")
	}
	if resumed != 1 || created != 1 {
		t.Fatalf("unexpected create/resume counts created=%d resumed=%d", created, resumed)
	}
}

func TestManagerRejectsToolFingerprintMismatch(t *testing.T) {
	manager := NewManager(5 * time.Minute)
	factory := func(context.Context) (gatewayruntime.Session, error) {
		return &fakeSession{id: "session"}, nil
	}

	lease, err := manager.Acquire(context.Background(), "tenant:session", Spec{Model: "gpt-4.1", Interactive: true, ToolsFingerprint: "tool-a"}, factory, nil, nil)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer lease.Release()

	if _, err := manager.Acquire(context.Background(), "tenant:session", Spec{Model: "gpt-4.1", Interactive: true, ToolsFingerprint: "tool-b"}, factory, nil, nil); err != ErrSessionSpecMismatch {
		t.Fatalf("expected ErrSessionSpecMismatch, got %v", err)
	}
}

func TestManagerRejectsPermissionModeMismatch(t *testing.T) {
	manager := NewManager(5 * time.Minute)
	factory := func(context.Context) (gatewayruntime.Session, error) {
		return &fakeSession{id: "session"}, nil
	}

	lease, err := manager.Acquire(context.Background(), "tenant:session", Spec{Model: "gpt-4.1", PermissionMode: gatewayruntime.PermissionModeBridge}, factory, nil, nil)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer lease.Release()

	if _, err := manager.Acquire(context.Background(), "tenant:session", Spec{Model: "gpt-4.1", PermissionMode: gatewayruntime.PermissionModeDeny}, factory, nil, nil); err != ErrSessionSpecMismatch {
		t.Fatalf("expected ErrSessionSpecMismatch, got %v", err)
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

	first, err := manager.Acquire(context.Background(), "ns:key-1", Spec{Model: "gpt-4.1"}, factory, nil, nil)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer first.Release()

	if _, err := manager.Acquire(context.Background(), "ns:key-2", Spec{Model: "gpt-4.1"}, factory, nil, nil); !errors.Is(err, ErrTooManySessionsForNamespace) {
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

	first, err := manager.Acquire(context.Background(), "ns1:key-1", Spec{Model: "gpt-4.1"}, factory, nil, nil)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer first.Release()

	if _, err := manager.Acquire(context.Background(), "ns2:key-2", Spec{Model: "gpt-4.1"}, factory, nil, nil); !errors.Is(err, ErrTooManySessions) {
		t.Fatalf("expected global limit error, got %v", err)
	}
}

func TestManagerResumesPersistedSessionAfterRestart(t *testing.T) {
	store := NewFileStore(t.TempDir() + "/sessions.json")
	spec := Spec{Model: "gpt-4.1"}

	created := 0
	firstManager := NewManagerWithStore(5*time.Minute, Limits{}, store)
	factory := func(context.Context) (gatewayruntime.Session, error) {
		created++
		return &fakeSession{id: "tenant:session"}, nil
	}

	lease, err := firstManager.Acquire(context.Background(), "tenant:session", spec, factory, nil, nil)
	if err != nil {
		t.Fatalf("initial acquire: %v", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("initial release: %v", err)
	}

	resumed := 0
	secondManager := NewManagerWithStore(5*time.Minute, Limits{}, store)
	resume := func(_ context.Context, sessionID string) (gatewayruntime.Session, error) {
		resumed++
		if sessionID != "tenant:session" {
			t.Fatalf("expected persisted session ID, got %q", sessionID)
		}
		return &fakeSession{id: sessionID}, nil
	}
	secondLease, err := secondManager.Acquire(context.Background(), "tenant:session", spec, factory, resume, nil)
	if err != nil {
		t.Fatalf("resume acquire: %v", err)
	}
	defer secondLease.Release()

	if secondLease.Created() {
		t.Fatalf("expected persisted session to be resumed")
	}
	if created != 1 {
		t.Fatalf("expected no extra session creation, got %d", created)
	}
	if resumed != 1 {
		t.Fatalf("expected one resume, got %d", resumed)
	}
}

func TestManagerRejectsPersistedSpecMismatch(t *testing.T) {
	store := NewFileStore(t.TempDir() + "/sessions.json")
	manager := NewManagerWithStore(5*time.Minute, Limits{}, store)
	factory := func(context.Context) (gatewayruntime.Session, error) {
		return &fakeSession{id: "tenant:session"}, nil
	}

	lease, err := manager.Acquire(context.Background(), "tenant:session", Spec{Model: "gpt-4.1"}, factory, nil, nil)
	if err != nil {
		t.Fatalf("initial acquire: %v", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("initial release: %v", err)
	}

	restarted := NewManagerWithStore(5*time.Minute, Limits{}, store)
	if _, err := restarted.Acquire(context.Background(), "tenant:session", Spec{Model: "claude"}, factory, nil, nil); !errors.Is(err, ErrSessionSpecMismatch) {
		t.Fatalf("expected persisted spec mismatch, got %v", err)
	}
}

func TestManagerRejectsPersistedSpecMismatchAndCleansStoredSession(t *testing.T) {
	store := NewFileStore(t.TempDir() + "/sessions.json")
	manager := NewManagerWithStore(5*time.Minute, Limits{}, store)
	factory := func(context.Context) (gatewayruntime.Session, error) {
		return &fakeSession{id: "tenant:session"}, nil
	}

	lease, err := manager.Acquire(context.Background(), "tenant:session", Spec{Model: "gpt-4.1"}, factory, nil, nil)
	if err != nil {
		t.Fatalf("initial acquire: %v", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("initial release: %v", err)
	}

	var deleted atomic.Int32
	deleteByID := func(context.Context, string) error {
		deleted.Add(1)
		return nil
	}
	restarted := NewManagerWithStore(5*time.Minute, Limits{}, store)
	if _, err := restarted.Acquire(context.Background(), "tenant:session", Spec{Model: "claude"}, factory, nil, deleteByID); !errors.Is(err, ErrSessionSpecMismatch) {
		t.Fatalf("expected persisted spec mismatch, got %v", err)
	}
	record, err := store.Load("tenant:session")
	if err != nil {
		t.Fatalf("load store after mismatch: %v", err)
	}
	if record != nil {
		t.Fatalf("expected mismatch cleanup to delete store record, got %#v", record)
	}
	if deleted.Load() != 1 {
		t.Fatalf("expected mismatch cleanup to delete backend session once, got %d", deleted.Load())
	}

	retry, err := restarted.Acquire(context.Background(), "tenant:session", Spec{Model: "claude"}, factory, nil, deleteByID)
	if err != nil {
		t.Fatalf("retry after mismatch cleanup: %v", err)
	}
	defer retry.Release()
	if !retry.Created() {
		t.Fatalf("expected retry to create a fresh session after mismatch cleanup")
	}
}

func TestManagerDoesNotDeleteActiveSessionOnConcurrentSpecMismatch(t *testing.T) {
	store := NewFileStore(t.TempDir() + "/sessions.json")
	manager := NewManagerWithStore(5*time.Minute, Limits{}, store)
	factory := func(context.Context) (gatewayruntime.Session, error) {
		return &fakeSession{id: "tenant:session"}, nil
	}

	lease, err := manager.Acquire(context.Background(), "tenant:session", Spec{Model: "gpt-4.1"}, factory, nil, nil)
	if err != nil {
		t.Fatalf("initial acquire: %v", err)
	}
	defer lease.Release()

	var deleted atomic.Int32
	deleteByID := func(context.Context, string) error {
		deleted.Add(1)
		return nil
	}
	if _, err := manager.Acquire(context.Background(), "tenant:session", Spec{Model: "claude"}, factory, nil, deleteByID); !errors.Is(err, ErrSessionSpecMismatch) {
		t.Fatalf("expected concurrent spec mismatch, got %v", err)
	}
	if deleted.Load() != 0 {
		t.Fatalf("expected active in-memory session to avoid backend deletion, got %d deletions", deleted.Load())
	}
}

func TestLookupWaitsForSessionInitialization(t *testing.T) {
	manager := NewManager(5 * time.Minute)
	readyToCreate := make(chan struct{})
	factory := func(context.Context) (gatewayruntime.Session, error) {
		<-readyToCreate
		return &fakeSession{id: "tenant:session"}, nil
	}

	leaseCh := make(chan *Lease, 1)
	errCh := make(chan error, 1)
	go func() {
		lease, err := manager.Acquire(context.Background(), "tenant:session", Spec{Model: "gpt-4.1"}, factory, nil, nil)
		if err != nil {
			errCh <- err
			return
		}
		leaseCh <- lease
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		manager.mu.Lock()
		_, exists := manager.sessions["tenant:session"]
		manager.mu.Unlock()
		if exists {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for in-flight session entry")
		}
		time.Sleep(5 * time.Millisecond)
	}

	lookupCh := make(chan gatewayruntime.Session, 1)
	go func() {
		lookupCh <- manager.Lookup("tenant:session")
	}()

	select {
	case <-lookupCh:
		t.Fatal("lookup returned before session initialization completed")
	case <-time.After(20 * time.Millisecond):
	}

	close(readyToCreate)

	var lease *Lease
	select {
	case err := <-errCh:
		t.Fatalf("acquire failed: %v", err)
	case lease = <-leaseCh:
	}
	defer lease.Release()

	select {
	case session := <-lookupCh:
		if session == nil || session.ID() != "tenant:session" {
			t.Fatalf("expected lookup to return initialized session, got %#v", session)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for lookup")
	}
}

type fakeSession struct {
	id         string
	closeCount int
}

func (f *fakeSession) ID() string { return f.id }

func (f *fakeSession) Send(context.Context, gatewayruntime.MessageOptions, gatewayruntime.EventHandler) (gatewayruntime.Result, error) {
	return gatewayruntime.Result{SessionID: f.id, Content: "ok"}, nil
}

func (f *fakeSession) ResolvePending(context.Context, gatewayruntime.PendingResponse) error {
	return nil
}

func (f *fakeSession) Close() error {
	f.closeCount++
	return nil
}
