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
}

type Manager struct {
	ttl                   time.Duration
	now                   func() time.Time
	maxActiveSessions     int
	maxActivePerNamespace int
	mu                    sync.Mutex
	sessions              map[string]*managedSession
}

type managedSession struct {
	key      string
	spec     Spec
	session  gatewayruntime.Session
	lastUsed atomic.Int64
	mu       sync.Mutex
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
	return NewManagerWithLimits(ttl, Limits{})
}

func NewManagerWithLimits(ttl time.Duration, limits Limits) *Manager {
	return &Manager{
		ttl:                   ttl,
		now:                   time.Now,
		maxActiveSessions:     limits.MaxActiveSessions,
		maxActivePerNamespace: limits.MaxActivePerNamespace,
		sessions:              make(map[string]*managedSession),
	}
}

func (m *Manager) Acquire(ctx context.Context, key string, spec Spec, factory func(context.Context) (gatewayruntime.Session, error)) (*Lease, error) {
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
			key:  key,
			spec: spec,
		}
		entry.mu.Lock()
		m.sessions[key] = entry
		m.mu.Unlock()

		sess, err := createSessionWithContext(ctx, factory)
		if err != nil {
			m.mu.Lock()
			current, ok := m.sessions[key]
			if ok && current == entry {
				delete(m.sessions, key)
			}
			m.mu.Unlock()
			entry.mu.Unlock()
			return nil, err
		}

		entry.session = sess
		entry.touch(m.now())
		return m.newLease(key, entry, true), nil
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
		if err := entry.session.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		entry.mu.Unlock()
	}
	return firstErr
}

func (m *managedSession) touch(now time.Time) {
	m.lastUsed.Store(now.UnixNano())
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
		if err := entry.session.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		entry.mu.Unlock()
	}
	return firstErr
}

func (m *Manager) newLease(key string, entry *managedSession, created bool) *Lease {
	return &Lease{
		session:    entry.session,
		sessionID:  entry.session.ID(),
		persistent: true,
		created:    created,
		release: func() error {
			entry.touch(m.now())
			entry.mu.Unlock()
			return nil
		},
		discard: func() error {
			m.mu.Lock()
			current, ok := m.sessions[key]
			if ok && current == entry {
				delete(m.sessions, key)
			}
			m.mu.Unlock()
			err := entry.session.Close()
			entry.mu.Unlock()
			return err
		},
	}
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
