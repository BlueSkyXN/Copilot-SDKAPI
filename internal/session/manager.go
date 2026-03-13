package session

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	gatewayruntime "copilot-sdkapi/internal/runtime"
)

var ErrSessionSpecMismatch = errors.New("session exists with different model or session settings")
var ErrTooManySessions = errors.New("too many active sessions")
var ErrTooManySessionsForNamespace = errors.New("too many active sessions for API key")

type Spec struct {
	Model            string
	SystemPrompt     string
	SystemPromptMode string
	ReasoningEffort  string
	Agent            string
	Interactive      bool
	ToolsFingerprint string
	PermissionMode   gatewayruntime.PermissionMode
}

type Manager struct {
	ttl                   time.Duration
	now                   func() time.Time
	maxActiveSessions     int
	maxActivePerNamespace int
	store                 Store
	mu                    sync.Mutex
	sessions              map[string]*managedSession
}

type managedSession struct {
	key        string
	spec       Spec
	sessionID  string
	session    gatewayruntime.Session
	deleteByID func(context.Context, string) error
	createdAt  time.Time
	lastUsed   atomic.Int64
	mu         sync.Mutex
	activityMu sync.Mutex
	activity   func()
	ready      chan struct{}
	readyOnce  sync.Once
}

const sessionLockRetryInterval = 5 * time.Millisecond

type Lease struct {
	session    gatewayruntime.Session
	sessionID  string
	persistent bool
	created    bool
	release    func() error
	discard    func() error
	once       sync.Once
	finalErr   error
}

type Limits struct {
	MaxActiveSessions     int
	MaxActivePerNamespace int
}

func NewManager(ttl time.Duration) *Manager {
	return NewManagerWithStore(ttl, Limits{}, nil)
}

func NewManagerWithLimits(ttl time.Duration, limits Limits) *Manager {
	return NewManagerWithStore(ttl, limits, nil)
}

func NewManagerWithStore(ttl time.Duration, limits Limits, store Store) *Manager {
	return &Manager{
		ttl:                   ttl,
		now:                   time.Now,
		maxActiveSessions:     limits.MaxActiveSessions,
		maxActivePerNamespace: limits.MaxActivePerNamespace,
		store:                 store,
		sessions:              make(map[string]*managedSession),
	}
}

func (m *Manager) Acquire(
	ctx context.Context,
	key string,
	spec Spec,
	factory func(context.Context) (gatewayruntime.Session, error),
	resume func(context.Context, string) (gatewayruntime.Session, error),
	deleteByID func(context.Context, string) error,
) (*Lease, error) {
	if key == "" {
		sess, err := createSessionWithContext(ctx, factory)
		if err != nil {
			return nil, err
		}
		return &Lease{
			session:    sess,
			sessionID:  sess.ID(),
			persistent: false,
			created:    true,
			release: func() error {
				return sess.Close()
			},
			discard: func() error {
				return sess.Close()
			},
		}, nil
	}

	for {
		m.mu.Lock()
		entry, exists := m.sessions[key]
		if exists {
			if entry.spec != spec {
				m.mu.Unlock()
				return nil, ErrSessionSpecMismatch
			}
			m.mu.Unlock()

			if err := lockManagedSession(ctx, entry); err != nil {
				return nil, err
			}
			m.mu.Lock()
			current, stillExists := m.sessions[key]
			if stillExists && current == entry {
				entry.touch(m.now())
				m.mu.Unlock()
				return m.newLease(key, entry, false), nil
			}
			m.mu.Unlock()
			entry.mu.Unlock()
			continue
		}

		if err := m.validateCapacityLocked(key); err != nil {
			m.mu.Unlock()
			return nil, err
		}

		entry = &managedSession{
			key:        key,
			spec:       spec,
			deleteByID: deleteByID,
			ready:      make(chan struct{}),
		}
		entry.mu.Lock()
		m.sessions[key] = entry
		m.mu.Unlock()
		readyMarked := false
		defer func() {
			if !readyMarked {
				entry.markReady()
			}
		}()
		cleanupEntry := func() {
			m.mu.Lock()
			current, ok := m.sessions[key]
			if ok && current == entry {
				delete(m.sessions, key)
			}
			m.mu.Unlock()
			entry.mu.Unlock()
		}
		var persisted *StoredSession
		if m.store != nil {
			record, err := m.store.Load(key)
			if err != nil {
				cleanupEntry()
				return nil, err
			}
			if record != nil {
				if record.Spec != spec {
					if err := m.removeStoredSession(ctx, key, record, deleteByID); err != nil {
						cleanupEntry()
						return nil, err
					}
					cleanupEntry()
					return nil, ErrSessionSpecMismatch
				}
				if m.ttl > 0 && m.now().Sub(record.UpdatedAt) >= m.ttl {
					if err := m.removeStoredSession(ctx, key, record, deleteByID); err != nil {
						cleanupEntry()
						return nil, err
					}
				} else {
					persisted = record
				}
			}
		}

		var (
			sess    gatewayruntime.Session
			err     error
			created = true
		)
		if persisted != nil && resume != nil {
			sess, err = createSessionWithContext(ctx, func(factoryCtx context.Context) (gatewayruntime.Session, error) {
				return resume(factoryCtx, persisted.SessionID)
			})
			if err != nil {
				if !errors.Is(err, gatewayruntime.ErrSessionNotFound) {
					cleanupEntry()
					return nil, err
				}
				if m.store != nil {
					if storeErr := m.store.Delete(key); storeErr != nil {
						cleanupEntry()
						return nil, storeErr
					}
				}
				persisted = nil
			} else {
				created = false
			}
		}
		if sess == nil {
			sess, err = createSessionWithContext(ctx, factory)
		}
		if err != nil {
			cleanupEntry()
			return nil, err
		}

		entry.sessionID = sess.ID()
		if persisted != nil && !persisted.CreatedAt.IsZero() {
			entry.createdAt = persisted.CreatedAt
		} else {
			entry.createdAt = m.now()
		}
		entry.session = sess
		entry.touch(m.now())
		if m.store != nil {
			now := m.now()
			record := StoredSession{
				SessionID: sess.ID(),
				Spec:      spec,
				CreatedAt: entry.createdAt,
				UpdatedAt: now,
			}
			if err := m.store.Save(key, record); err != nil {
				_ = entry.session.Close()
				cleanupEntry()
				return nil, err
			}
		}
		entry.markReady()
		readyMarked = true
		return m.newLease(key, entry, created), nil
	}
}

func (m *Manager) validateCapacityLocked(key string) error {
	if m.maxActiveSessions > 0 && len(m.sessions) >= m.maxActiveSessions {
		return ErrTooManySessions
	}
	if m.maxActivePerNamespace <= 0 {
		return nil
	}

	namespace := sessionNamespace(key)
	active := 0
	for existingKey := range m.sessions {
		if sessionNamespace(existingKey) == namespace {
			active++
		}
	}
	if active >= m.maxActivePerNamespace {
		return ErrTooManySessionsForNamespace
	}
	return nil
}

func (m *Manager) CleanupExpired() error {
	if m.ttl <= 0 {
		return nil
	}

	now := m.now()
	stale := make([]*managedSession, 0)

	m.mu.Lock()
	for key, entry := range m.sessions {
		if now.Sub(entry.lastUsedAt()) < m.ttl {
			continue
		}
		if !entry.mu.TryLock() {
			continue
		}
		delete(m.sessions, key)
		stale = append(stale, entry)
	}
	m.mu.Unlock()

	var firstErr error
	for _, entry := range stale {
		if err := m.destroyPersistentSession(context.Background(), entry); err != nil && firstErr == nil {
			firstErr = err
		}
		entry.mu.Unlock()
	}
	return firstErr
}

func (m *managedSession) touch(now time.Time) {
	m.lastUsed.Store(now.UnixNano())
}

func (m *managedSession) waitReady() {
	if m.ready == nil {
		return
	}
	<-m.ready
}

func (m *managedSession) markReady() {
	m.readyOnce.Do(func() {
		if m.ready != nil {
			close(m.ready)
		}
	})
}

func lockManagedSession(ctx context.Context, entry *managedSession) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if entry.mu.TryLock() {
		return nil
	}

	ticker := time.NewTicker(sessionLockRetryInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if entry.mu.TryLock() {
				return nil
			}
		}
	}
}

func (m *managedSession) lastUsedAt() time.Time {
	return time.Unix(0, m.lastUsed.Load())
}

func (m *Manager) Close() error {
	return m.CloseContext(context.Background())
}

func (m *Manager) CloseContext(ctx context.Context) error {
	return m.closeContext(ctx, false)
}

func (m *Manager) DisconnectContext(ctx context.Context) error {
	return m.closeContext(ctx, true)
}

func (m *Manager) closeContext(ctx context.Context, preservePersistent bool) error {
	m.mu.Lock()
	entries := make([]*managedSession, 0, len(m.sessions))
	for key, entry := range m.sessions {
		delete(m.sessions, key)
		entries = append(entries, entry)
	}
	m.mu.Unlock()

	var firstErr error
	for _, entry := range entries {
		if err := lockManagedSession(ctx, entry); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		var err error
		if preservePersistent {
			err = m.disconnectPersistentSession(entry)
		} else {
			err = m.destroyPersistentSession(ctx, entry)
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
		entry.mu.Unlock()
	}
	return firstErr
}

func (m *Manager) newLease(key string, entry *managedSession, created bool) *Lease {
	return &Lease{
		session:    entry.session,
		sessionID:  entry.sessionID,
		persistent: true,
		created:    created,
		release: func() error {
			now := m.now()
			entry.touch(now)
			var err error
			if m.store != nil {
				err = m.store.Save(key, StoredSession{
					SessionID: entry.sessionID,
					Spec:      entry.spec,
					CreatedAt: entry.createdAt,
					UpdatedAt: now,
				})
			}
			entry.mu.Unlock()
			return err
		},
		discard: func() error {
			m.mu.Lock()
			current, ok := m.sessions[key]
			if ok && current == entry {
				delete(m.sessions, key)
			}
			m.mu.Unlock()
			err := m.destroyPersistentSession(context.Background(), entry)
			entry.mu.Unlock()
			return err
		},
	}
}

func (m *Manager) destroyPersistentSession(ctx context.Context, entry *managedSession) error {
	var firstErr error
	if entry.session != nil {
		if err := entry.session.Close(); err != nil && !errors.Is(err, gatewayruntime.ErrSessionNotFound) && firstErr == nil {
			firstErr = err
		}
	}
	if entry.key != "" && m.store != nil {
		if err := m.store.Delete(entry.key); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if entry.sessionID != "" && entry.deleteByID != nil {
		if err := entry.deleteByID(ctx, entry.sessionID); err != nil && !errors.Is(err, gatewayruntime.ErrSessionNotFound) && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (m *Manager) disconnectPersistentSession(entry *managedSession) error {
	if entry.session == nil {
		return nil
	}
	if err := entry.session.Close(); err != nil && !errors.Is(err, gatewayruntime.ErrSessionNotFound) {
		return err
	}
	return nil
}

func createSessionWithContext(ctx context.Context, factory func(context.Context) (gatewayruntime.Session, error)) (gatewayruntime.Session, error) {
	type result struct {
		session gatewayruntime.Session
		err     error
	}

	resultCh := make(chan result, 1)
	go func() {
		session, err := factory(ctx)
		resultCh <- result{session: session, err: err}
	}()

	select {
	case <-ctx.Done():
		go func() {
			res := <-resultCh
			if res.session != nil {
				_ = res.session.Close()
			}
		}()
		return nil, ctx.Err()
	case res := <-resultCh:
		return res.session, res.err
	}
}

func sessionNamespace(key string) string {
	if prefix, _, found := strings.Cut(key, ":"); found {
		return prefix
	}
	return key
}

func (m *Manager) Lookup(key string) gatewayruntime.Session {
	if strings.TrimSpace(key) == "" {
		return nil
	}
	m.mu.Lock()
	entry := m.sessions[key]
	m.mu.Unlock()
	if entry == nil {
		return nil
	}
	entry.waitReady()
	return entry.session
}

func (m *Manager) SetActivityHook(key string, hook func()) func() {
	if strings.TrimSpace(key) == "" {
		return func() {}
	}
	m.mu.Lock()
	entry := m.sessions[key]
	m.mu.Unlock()
	if entry == nil {
		return func() {}
	}
	entry.activityMu.Lock()
	entry.activity = hook
	entry.activityMu.Unlock()
	return func() {
		entry.activityMu.Lock()
		if entry.activity != nil {
			entry.activity = nil
		}
		entry.activityMu.Unlock()
	}
}

func (m *Manager) TouchActivity(key string) {
	if strings.TrimSpace(key) == "" {
		return
	}
	m.mu.Lock()
	entry := m.sessions[key]
	m.mu.Unlock()
	if entry == nil {
		return
	}
	entry.waitReady()
	entry.activityMu.Lock()
	hook := entry.activity
	entry.activityMu.Unlock()
	if hook != nil {
		hook()
	}
}

func (m *Manager) removeStoredSession(ctx context.Context, key string, record *StoredSession, deleteByID func(context.Context, string) error) error {
	if record == nil || m.store == nil {
		return nil
	}
	if err := m.store.Delete(key); err != nil {
		return err
	}
	if deleteByID != nil {
		if err := deleteByID(ctx, record.SessionID); err != nil && !errors.Is(err, gatewayruntime.ErrSessionNotFound) {
			return err
		}
	}
	return nil
}

func (l *Lease) Session() gatewayruntime.Session {
	return l.session
}

func (l *Lease) SessionID() string {
	return l.sessionID
}

func (l *Lease) Persistent() bool {
	return l.persistent
}

func (l *Lease) Created() bool {
	return l.created
}

func (l *Lease) Release() error {
	if l.release == nil {
		return nil
	}
	l.once.Do(func() {
		l.finalErr = l.release()
	})
	return l.finalErr
}

func (l *Lease) Discard() error {
	if l.discard == nil {
		return l.Release()
	}
	l.once.Do(func() {
		l.finalErr = l.discard()
	})
	return l.finalErr
}
