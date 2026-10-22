// Package blob coordinates physical object readers with storage maintenance.
// Authorization belongs to callers; a lease is retention, never read permission.
package blob

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"cloudenest_go/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrReadUnavailable = errors.New("storage read protection unavailable")

const Active = "active"

type Manager struct {
	db                                *gorm.DB
	ttl, renewEvery, operationTimeout time.Duration
}

// NewManager uses database UTC for expiry and conservative monotonic local
// deadlines for readers. Timing is not controlled by HTTP or environment input.
func NewManager(db *gorm.DB) *Manager {
	m, _ := NewManagerWithTiming(db, 60*time.Second, 20*time.Second)
	return m
}

// NewManagerWithTiming supports short, real expiry tests. Production uses NewManager.
func NewManagerWithTiming(db *gorm.DB, ttl, renewEvery time.Duration) (*Manager, error) {
	if ttl < time.Second || renewEvery <= 0 || renewEvery >= ttl/2 {
		return nil, errors.New("invalid read lease timing")
	}
	timeout := 5 * time.Second
	if ttl/4 < timeout {
		timeout = ttl / 4
	}
	return &Manager{db: db, ttl: ttl, renewEvery: renewEvery, operationTimeout: timeout}, nil
}

type scopeKey struct{}
type heldLease struct {
	token    string
	object   model.Storage
	deadline time.Time
}

type Scope struct {
	manager    *Manager
	ctx        context.Context
	cancel     context.CancelFunc
	mu         sync.Mutex
	held       map[int64]heldLease
	earliest   time.Time
	err        error
	closed     bool
	stop, done chan struct{}
	started    bool
	closeOnce  sync.Once
	closeErr   error
}

func (m *Manager) Begin(parent context.Context) (context.Context, *Scope) {
	ctx, cancel := context.WithCancel(parent)
	s := &Scope{manager: m, ctx: ctx, cancel: cancel, held: make(map[int64]heldLease), stop: make(chan struct{}), done: make(chan struct{})}
	return context.WithValue(ctx, scopeKey{}, s), s
}

// Pin returns the exact immutable path protected by this scope. Repeated opens
// of the same object reuse its lease/path, even if an upload repairs the pointer.
func Pin(ctx context.Context, object model.Storage) (model.Storage, error) {
	s, ok := ctx.Value(scopeKey{}).(*Scope)
	if !ok || s == nil {
		return model.Storage{}, ErrReadUnavailable
	}
	return s.pin(object.ID)
}

func Check(ctx context.Context) error {
	if s, ok := ctx.Value(scopeKey{}).(*Scope); ok {
		return s.Check()
	}
	return ErrReadUnavailable
}

func (s *Scope) checkLocked() error {
	if s.err != nil {
		return s.err
	}
	if s.closed {
		return ErrReadUnavailable
	}
	if err := s.ctx.Err(); err != nil {
		return err
	}
	if !s.earliest.IsZero() && !time.Now().Before(s.earliest) {
		return ErrReadUnavailable
	}
	return nil
}
func (s *Scope) Check() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkLocked(); err != nil {
		return s.failLocked(err)
	}
	return nil
}

func (s *Scope) failLocked(err error) error {
	if s.err != nil {
		return s.err
	}
	s.err = fmt.Errorf("%w: %w", ErrReadUnavailable, err)
	s.cancel()
	return s.err
}
func (s *Scope) updateEarliestLocked() {
	s.earliest = time.Time{}
	for _, h := range s.held {
		if s.earliest.IsZero() || h.deadline.Before(s.earliest) {
			s.earliest = h.deadline
		}
	}
}

func (s *Scope) pin(id int64) (model.Storage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkLocked(); err != nil {
		return model.Storage{}, s.failLocked(err)
	}
	if h, ok := s.held[id]; ok {
		return h.object, nil
	}
	if id <= 0 || s.manager.db == nil {
		return model.Storage{}, s.failLocked(ErrReadUnavailable)
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return model.Storage{}, s.failLocked(err)
	}
	token := hex.EncodeToString(raw)
	started := time.Now()
	ctx, cancel := context.WithTimeout(s.ctx, s.manager.operationTimeout)
	defer cancel()
	var object model.Storage
	err := s.manager.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).First(&object, id).Error; err != nil {
			return err
		}
		if object.State != Active {
			return ErrReadUnavailable
		}
		return tx.Exec("INSERT INTO storage_read_lease (token,storage_id,expires_at) VALUES (?,?,DATE_ADD(UTC_TIMESTAMP(6),INTERVAL ? MICROSECOND))", token, id, s.manager.ttl.Microseconds()).Error
	})
	if err != nil {
		return model.Storage{}, s.failLocked(err)
	}
	// Start before the DB round trip. Delays can only shorten local protection;
	// the client never assumes a lease lasts longer than the database granted.
	h := heldLease{token: token, object: object, deadline: started.Add(s.manager.ttl * 4 / 5)}
	s.held[id] = h
	s.updateEarliestLocked()
	if err := s.checkLocked(); err != nil {
		return model.Storage{}, s.failLocked(err)
	}
	if !s.started {
		s.started = true
		go s.renewLoop()
	}
	return object, nil
}

func (s *Scope) renewLoop() {
	defer close(s.done)
	timer := time.NewTicker(s.manager.renewEvery)
	defer timer.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-s.ctx.Done():
			return
		case <-timer.C:
			if !s.renew() {
				return
			}
		}
	}
}

func (s *Scope) renew() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkLocked(); err != nil {
		s.failLocked(err)
		return false
	}
	// One parent at a time: no cross-object lock ordering and no long transaction
	// across a large archive. Expired leases are never resurrected.
	for id, h := range s.held {
		started := time.Now()
		if !started.Before(h.deadline) {
			s.failLocked(ErrReadUnavailable)
			return false
		}
		ctx, cancel := context.WithTimeout(s.ctx, s.manager.operationTimeout)
		err := s.manager.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var object model.Storage
			if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).First(&object, id).Error; err != nil {
				return err
			}
			if object.State != Active {
				return ErrReadUnavailable
			}
			result := tx.Exec("UPDATE storage_read_lease SET expires_at=DATE_ADD(UTC_TIMESTAMP(6),INTERVAL ? MICROSECOND) WHERE token=? AND storage_id=? AND expires_at>UTC_TIMESTAMP(6)", s.manager.ttl.Microseconds(), h.token, id)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrReadUnavailable
			}
			return nil
		})
		cancel()
		if err != nil {
			s.failLocked(err)
			return false
		}
		if !time.Now().Before(h.deadline) {
			s.failLocked(ErrReadUnavailable)
			return false
		}
		h.deadline = started.Add(s.manager.ttl * 4 / 5)
		s.held[id] = h
	}
	s.updateEarliestLocked()
	return true
}

// Close waits for this scope's heartbeat and removes only its own tokens. A
// failed cleanup leaves TTL-protected rows for later pruning; never clear a table.
func (s *Scope) Close() error {
	s.closeOnce.Do(func() {
		s.cancel()
		s.mu.Lock()
		s.closed = true
		started := s.started
		close(s.stop)
		s.mu.Unlock()
		if started {
			<-s.done
		}
		s.mu.Lock()
		tokens := make([]string, 0, len(s.held))
		for _, h := range s.held {
			tokens = append(tokens, h.token)
		}
		s.mu.Unlock()
		if len(tokens) == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.closeErr = s.manager.db.WithContext(ctx).Where("token IN ?", tokens).Delete(&model.StorageReadLease{}).Error
	})
	return s.closeErr
}
