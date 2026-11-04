// Package agentauth owns independent device credentials and the transactions
// that issue, rotate, revoke and use them. User JWTs are a separate namespace.
package agentauth

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"cloudenest_go/model"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrInput  = errors.New("invalid device input")
	ErrDenied = errors.New("invalid device credentials")
	ErrOwner  = errors.New("device unavailable to owner")
	ErrReauth = errors.New("password verification failed")
	ErrBusy   = errors.New("device authentication busy")
)

const EnrollmentTTL = 10 * time.Minute
const HeartbeatTTL = 90 * time.Second

var reauthSlots = make(chan struct{}, 4)

type Manager struct{ db *gorm.DB }

func New(db *gorm.DB) *Manager { return &Manager{db: db} }

type Issued struct {
	DeviceID   int64     `json:"device_id"`
	Enrollment string    `json:"enrollment"`
	ExpiresAt  time.Time `json:"expires_at"`
}
type Registered struct {
	DeviceID   int64  `json:"device_id"`
	Credential string `json:"credential"`
}
type Heartbeat struct {
	DeviceID   int64     `json:"device_id"`
	ReceivedAt time.Time `json:"received_at"`
}

func lockOwnerAndDevice(tx *gorm.DB, ownerID, deviceID int64) (model.User, error) {
	var owner model.User
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&owner, "id=?", ownerID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) || err == nil && owner.Status != 1 {
		return model.User{}, ErrOwner
	}
	if err != nil {
		return model.User{}, err
	}
	var device model.Device
	err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&device, "id=? AND user_id=? AND status=1", deviceID, ownerID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.User{}, ErrOwner
	}
	return owner, err
}

func verifyOwner(tx *gorm.DB, ownerID, deviceID int64, password string) error {
	owner, err := lockOwnerAndDevice(tx, ownerID, deviceID)
	if err != nil {
		return err
	}
	if len(password) == 0 || len(password) > 72 {
		return ErrReauth
	}
	select {
	case reauthSlots <- struct{}{}:
		defer func() { <-reauthSlots }()
	default:
		return ErrBusy
	}
	if bcrypt.CompareHashAndPassword([]byte(owner.Password), []byte(password)) != nil {
		return ErrReauth
	}
	return nil
}

func audit(tx *gorm.DB, ownerID, deviceID int64, action string) error {
	return tx.Create(&model.OperationLog{UserID: &ownerID, Action: action, Status: "success", Detail: fmt.Sprintf("device_id=%d", deviceID)}).Error
}

func (m *Manager) Issue(ctx context.Context, ownerID, deviceID int64, password string) (Issued, error) {
	var result Issued
	err := m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := verifyOwner(tx, ownerID, deviceID, password); err != nil {
			return err
		}
		t, err := issueToken(enrollmentPrefix)
		if err != nil {
			return err
		}
		expiry := time.Now().UTC().Add(EnrollmentTTL).Truncate(time.Microsecond)
		// Serialize issuance on the device. A new enrollment supersedes a prior
		// pending registration; the active credential rotates only upon exchange.
		var binding model.AgentBinding
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&binding, "device_id=?", deviceID).Error
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			binding = model.AgentBinding{DeviceID: deviceID, OwnerID: ownerID}
		case err != nil:
			return err
		case binding.OwnerID != ownerID:
			binding = model.AgentBinding{DeviceID: deviceID, OwnerID: ownerID}
		}
		binding.EnrollmentID = &t.id
		binding.EnrollmentDigest = t.digest[:]
		binding.EnrollmentExpires = &expiry
		if err = tx.Save(&binding).Error; err != nil {
			return err
		}
		if err = audit(tx, ownerID, deviceID, "agent.enrollment.issue"); err != nil {
			return err
		}
		result = Issued{DeviceID: deviceID, Enrollment: t.raw, ExpiresAt: expiry}
		return nil
	})
	if err != nil {
		return Issued{}, err
	}
	return result, nil
}

// The first lookup discovers lock keys only. After locking owner and device,
// re-read the binding under lock before comparing a digest or changing state.
func lockBinding(tx *gorm.DB, publicID string, enrollment bool) (model.AgentBinding, error) {
	column := "credential_id"
	if enrollment {
		column = "enrollment_id"
	}
	var hint model.AgentBinding
	err := tx.Select("device_id,owner_id").First(&hint, column+"=?", publicID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return hint, ErrDenied
	}
	if err != nil {
		return hint, err
	}
	if _, err = lockOwnerAndDevice(tx, hint.OwnerID, hint.DeviceID); err != nil {
		if errors.Is(err, ErrOwner) {
			return hint, ErrDenied
		}
		return hint, err
	}
	var current model.AgentBinding
	err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, "device_id=? AND owner_id=? AND "+column+"=?", hint.DeviceID, hint.OwnerID, publicID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return current, ErrDenied
	}
	return current, err
}

func (m *Manager) Exchange(ctx context.Context, raw string) (Registered, error) {
	input, ok := parseToken(enrollmentPrefix, raw)
	if !ok {
		return Registered{}, ErrDenied
	}
	var result Registered
	err := m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		b, err := lockBinding(tx, input.id, true)
		if err != nil {
			return err
		}
		if subtle.ConstantTimeCompare(b.EnrollmentDigest, input.digest[:]) != 1 || b.EnrollmentExpires == nil || !b.EnrollmentExpires.After(time.Now().UTC()) {
			return ErrDenied
		}
		key, err := issueToken(credentialPrefix)
		if err != nil {
			return err
		}
		b.EnrollmentID = nil
		b.EnrollmentDigest = nil
		b.EnrollmentExpires = nil
		b.CredentialID = &key.id
		b.CredentialDigest = key.digest[:]
		b.LastSeenAt = nil
		b.ClientVersion = ""
		if err = tx.Save(&b).Error; err != nil {
			return err
		}
		if err = audit(tx, b.OwnerID, b.DeviceID, "agent.enrollment.exchange"); err != nil {
			return err
		}
		result = Registered{DeviceID: b.DeviceID, Credential: key.raw}
		return nil
	})
	if err != nil {
		return Registered{}, err
	}
	return result, nil
}

func (m *Manager) Revoke(ctx context.Context, ownerID, deviceID int64, password string) error {
	return m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := verifyOwner(tx, ownerID, deviceID, password); err != nil {
			return err
		}
		err := tx.Model(&model.AgentBinding{}).Where("device_id=? AND owner_id=?", deviceID, ownerID).Updates(map[string]any{
			"enrollment_id": nil, "enrollment_digest": nil, "enrollment_expires": nil, "credential_id": nil, "credential_digest": nil, "last_seen_at": nil, "client_version": "",
		}).Error
		if err != nil {
			return err
		}
		return audit(tx, ownerID, deviceID, "agent.credential.revoke")
	})
}

func (m *Manager) Beat(ctx context.Context, raw, version string) (Heartbeat, error) {
	input, ok := parseToken(credentialPrefix, raw)
	if !ok {
		return Heartbeat{}, ErrDenied
	}
	if len(version) > 64 {
		return Heartbeat{}, ErrInput
	}
	for _, r := range version {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == '+' || r == '_') {
			return Heartbeat{}, ErrInput
		}
	}
	var result Heartbeat
	err := m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		b, err := lockBinding(tx, input.id, false)
		if err != nil {
			return err
		}
		if subtle.ConstantTimeCompare(b.CredentialDigest, input.digest[:]) != 1 {
			return ErrDenied
		}
		now := time.Now().UTC().Truncate(time.Microsecond)
		if err = tx.Model(&b).Updates(map[string]any{"last_seen_at": now, "client_version": version}).Error; err != nil {
			return err
		}
		result = Heartbeat{DeviceID: b.DeviceID, ReceivedAt: now}
		return nil
	})
	if err != nil {
		return Heartbeat{}, err
	}
	return result, nil
}

// WithCredential serializes a device operation with rotation, revocation and
// owner/device lifecycle changes. Callbacks must use this transaction only.
func (m *Manager) WithCredential(ctx context.Context, raw string, fn func(*gorm.DB, model.AgentBinding) error) error {
	input, ok := parseToken(credentialPrefix, raw)
	if !ok {
		return ErrDenied
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		binding, err := lockBinding(tx, input.id, false)
		if err != nil {
			return err
		}
		if subtle.ConstantTimeCompare(binding.CredentialDigest, input.digest[:]) != 1 {
			return ErrDenied
		}
		return fn(tx, binding)
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
}

// WithManagedDevice uses the same owner -> device -> binding lock order as
// device authentication. An empty binding permits read-only management history.
// READ COMMITTED prevents the initial ownership hint from freezing a snapshot
// before waiting for these locks, so queue counts and deduplication see commits.
func (m *Manager) WithManagedDevice(ctx context.Context, actorID int64, admin bool, deviceID int64, fn func(*gorm.DB, model.AgentBinding) error) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var hint model.Device
		if err := tx.First(&hint, deviceID).Error; err != nil {
			return err
		}
		if !admin && hint.UserID != actorID {
			return ErrOwner
		}
		if _, err := lockOwnerAndDevice(tx, hint.UserID, deviceID); err != nil {
			return err
		}
		var binding model.AgentBinding
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&binding, "device_id=? AND owner_id=?", deviceID, hint.UserID).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			binding = model.AgentBinding{DeviceID: deviceID, OwnerID: hint.UserID}
		} else if err != nil {
			return err
		}
		return fn(tx, binding)
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
}
