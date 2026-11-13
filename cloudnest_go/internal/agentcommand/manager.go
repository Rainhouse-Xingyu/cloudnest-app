package agentcommand

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"cloudenest_go/internal/agentauth"
	"cloudenest_go/internal/agentstatus"
	"cloudenest_go/internal/frpconfig"
	"cloudenest_go/internal/frpreservation"
	"cloudenest_go/internal/frpserver"
	"cloudenest_go/internal/service/contract"
	"cloudenest_go/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrInput = errors.New("invalid command input")
var ErrConflict = errors.New("command state conflict")
var ErrUnregistered = errors.New("device registration required")
var ErrFull = errors.New("device command queue full")

const LeaseTTL = 60 * time.Second
const Lifetime = 24 * time.Hour
const MaxAttempts = 5
const MaxPending = 32

type Manager struct {
	auth *agentauth.Manager
	keys *frpconfig.Keyring
}

func New(db *gorm.DB, keys *frpconfig.Keyring) *Manager {
	return &Manager{auth: agentauth.New(db), keys: keys}
}

func ValidID(s string) bool {
	if len(s) != 32 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func randomID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}
func nowDB(tx *gorm.DB) (time.Time, error) {
	var micros int64
	err := tx.Raw("SELECT TIMESTAMPDIFF(MICROSECOND,'1970-01-01 00:00:00',UTC_TIMESTAMP(6))").Scan(&micros).Error
	return time.UnixMicro(micros).UTC(), err
}
func utc(t time.Time) model.CommandTime { return model.CommandTime{Time: t.UTC()} }

func audit(tx *gorm.DB, actor *int64, id, action, ip string) error {
	return tx.Create(&model.OperationLog{UserID: actor, Action: "agent.command." + action, Detail: "command_id=" + id, ClientIP: ip}).Error
}
func credential(b model.AgentBinding) string {
	if b.CredentialID == nil {
		return ""
	}
	return *b.CredentialID
}
func live(s string) bool { return s == "pending" || s == "leased" }
func allowed(action string) bool {
	switch action {
	case "deploy_config", "start", "stop", "reload", "diagnose", "forget_config":
		return true
	}
	return false
}

// Invalidation is immediate through the credential fence, even before these
// housekeeping updates run. Old work can never be inherited after reenrollment.
func expire(tx *gorm.DB, b model.AgentBinding, now time.Time) error {
	q := tx.Model(&model.AgentCommand{}).Where("device_id=? AND status IN ?", b.DeviceID, []string{"pending", "leased"})
	if err := q.Where("credential_id<>?", credential(b)).Updates(map[string]any{"status": "cancelled", "completed_at": utc(now)}).Error; err != nil {
		return err
	}
	return tx.Model(&model.AgentCommand{}).Where("device_id=? AND credential_id=? AND status IN ? AND (expires_at<=? OR (status='leased' AND lease_expires<=? AND attempts>=?))", b.DeviceID, credential(b), []string{"pending", "leased"}, utc(now), utc(now), MaxAttempts).Updates(map[string]any{"status": "timed_out", "completed_at": utc(now)}).Error
}

type EnqueueInput struct {
	RequestID string `json:"request_id"`
	Action    string `json:"action"`
	ConfigID  int64  `json:"config_id"`
}

func (m *Manager) Enqueue(ctx context.Context, actor int64, admin bool, device int64, input EnqueueInput, ip string) (model.AgentCommand, error) {
	if device <= 0 || !ValidID(input.RequestID) || !allowed(input.Action) || input.ConfigID < 0 || input.Action != "deploy_config" && input.ConfigID != 0 {
		return model.AgentCommand{}, ErrInput
	}
	var out model.AgentCommand
	err := m.auth.WithManagedDevice(ctx, actor, admin, device, func(tx *gorm.DB, b model.AgentBinding) error {
		if credential(b) == "" {
			return ErrUnregistered
		}
		now, err := nowDB(tx)
		if err != nil {
			return err
		}
		if err := expire(tx, b, now); err != nil {
			return err
		}
		var old model.AgentCommand
		err = tx.Where("device_id=? AND request_id=?", device, input.RequestID).First(&old).Error
		if err == nil {
			if old.CredentialID != credential(b) || old.Action != input.Action || old.RequestedConfigID != input.ConfigID {
				return ErrConflict
			}
			out = old
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var count int64
		if err := tx.Model(&model.AgentCommand{}).Where("device_id=? AND status IN ?", device, []string{"pending", "leased"}).Count(&count).Error; err != nil {
			return err
		}
		if count >= MaxPending {
			return ErrFull
		}
		// Forget is an exclusive barrier. A successful receipt proves the
		// conforming Agent stopped and durably removed its restart config.
		// It must not race an already queued deployment or a later start.
		if input.Action == "forget_config" {
			if count != 0 || b.ClientVersion != "cloudnest-agent-0.3.0" || b.LastSeenAt == nil || b.LastSeenAt.Before(now.Add(-90*time.Second)) || b.LastSeenAt.After(now.Add(5*time.Second)) {
				return ErrConflict
			}
		} else {
			var barriers int64
			if err := tx.Model(&model.AgentCommand{}).Where("device_id=? AND status IN ? AND action='forget_config'", device, []string{"pending", "leased"}).Count(&barriers).Error; err != nil {
				return err
			}
			if barriers != 0 {
				return ErrConflict
			}
		}
		id, err := randomID()
		if err != nil {
			return err
		}
		out = model.AgentCommand{ID: id, DeviceID: device, CredentialID: credential(b), RequestID: input.RequestID, RequestedConfigID: input.ConfigID, Action: input.Action, Status: "pending", CreatedAt: utc(now), ExpiresAt: utc(now.Add(Lifetime))}
		if input.Action == "deploy_config" {
			var cfg model.FrpConfig
			q := tx.Where("device_id=?", device)
			if input.ConfigID > 0 {
				q = q.Where("id=?", input.ConfigID)
			}
			if err := q.Order("id DESC").First(&cfg).Error; err != nil {
				return err
			}
			if cfg.ServerID != 0 || cfg.ServerRevisionID != 0 {
				if err := frpserver.CheckAccess(tx, b.OwnerID, cfg.ServerID, cfg.ServerRevisionID); err != nil {
					if errors.Is(err, contract.ErrForbidden) || errors.Is(err, contract.ErrConflict) || errors.Is(err, contract.ErrInvalidInput) || errors.Is(err, gorm.ErrRecordNotFound) {
						return ErrConflict
					}
					return err
				}
			}
			plain, err := m.keys.OpenConfig(device, cfg.ID, cfg.ServerID, cfg.ServerRevisionID, cfg.ConfigText)
			if err != nil {
				return err
			}
			if err := frpreservation.Acquire(tx, b.OwnerID, device, credential(b), out.ID, cfg, plain); err != nil {
				return err
			}
			out.ConfigID = cfg.ID
			out.ConfigSHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte(plain)))
		}
		if err := tx.Create(&out).Error; err != nil {
			return err
		}
		return audit(tx, &actor, id, "queued", ip)
	}, frpreservation.Lock)
	if err != nil {
		return model.AgentCommand{}, err
	}
	return out, nil
}

type Delivery struct {
	Command model.AgentCommand `json:"command"`
	LeaseID string             `json:"lease_id"`
}

// A claim ID must be retained across a lost HTTP response. Different claim IDs
// do not receive an already active lease. Agent execution still needs a journal.
func (m *Manager) Claim(ctx context.Context, key, claimID string) (*Delivery, error) {
	if !ValidID(claimID) {
		return nil, ErrInput
	}
	var out *Delivery
	err := m.auth.WithCredential(ctx, key, func(tx *gorm.DB, b model.AgentBinding) error {
		now, err := nowDB(tx)
		if err != nil {
			return err
		}
		if err := expire(tx, b, now); err != nil {
			return err
		}
		var row model.AgentCommand
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("device_id=? AND credential_id=? AND status IN ?", b.DeviceID, credential(b), []string{"pending", "leased"}).Order("CASE WHEN status='leased' THEN 0 ELSE 1 END,created_at,id").First(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if row.Status == "leased" && row.LeaseExpires != nil && row.LeaseExpires.After(now) {
			if row.ClaimID == claimID {
				out = &Delivery{Command: row, LeaseID: row.LeaseID}
			}
			return nil
		}
		lease, err := randomID()
		if err != nil {
			return err
		}
		until := now.Add(LeaseTTL)
		if until.After(row.ExpiresAt.Time) {
			until = row.ExpiresAt.Time
		}
		row.Status = "leased"
		row.Attempts++
		row.ClaimID = claimID
		row.LeaseID = lease
		leaseTime := utc(until)
		row.LeaseExpires = &leaseTime
		if err := tx.Save(&row).Error; err != nil {
			return err
		}
		if err := audit(tx, nil, row.ID, "leased", ""); err != nil {
			return err
		}
		out = &Delivery{Command: row, LeaseID: lease}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func lockedCommand(tx *gorm.DB, b model.AgentBinding, id, lease string) (model.AgentCommand, error) {
	var row model.AgentCommand
	if !ValidID(id) || !ValidID(lease) {
		return row, ErrInput
	}
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND device_id=? AND credential_id=?", id, b.DeviceID, credential(b)).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return row, ErrConflict
	}
	if err != nil {
		return row, err
	}
	if row.LeaseID != lease {
		return row, ErrConflict
	}
	return row, nil
}
func active(row model.AgentCommand, now time.Time) bool {
	return row.Status == "leased" && row.LeaseExpires != nil && row.LeaseExpires.After(now) && row.ExpiresAt.After(now)
}

func (m *Manager) Renew(ctx context.Context, key, id, lease string) (time.Time, error) {
	var until time.Time
	err := m.auth.WithCredential(ctx, key, func(tx *gorm.DB, b model.AgentBinding) error {
		row, err := lockedCommand(tx, b, id, lease)
		if err != nil {
			return err
		}
		now, err := nowDB(tx)
		if err != nil {
			return err
		}
		if !active(row, now) {
			return ErrConflict
		}
		until = now.Add(LeaseTTL)
		if until.After(row.ExpiresAt.Time) {
			until = row.ExpiresAt.Time
		}
		return tx.Model(&row).Update("lease_expires", utc(until)).Error
	})
	return until, err
}

type Configuration struct {
	ConfigID int64  `json:"config_id"`
	Text     string `json:"config_text"`
	SHA256   string `json:"sha256"`
}

func (m *Manager) Configuration(ctx context.Context, key, id, lease string) (Configuration, error) {
	var out Configuration
	err := m.auth.WithCredential(ctx, key, func(tx *gorm.DB, b model.AgentBinding) error {
		row, err := lockedCommand(tx, b, id, lease)
		if err != nil {
			return err
		}
		now, err := nowDB(tx)
		if err != nil {
			return err
		}
		if !active(row, now) || row.Action != "deploy_config" {
			return ErrConflict
		}
		var cfg model.FrpConfig
		if err := tx.Where("id=? AND device_id=?", row.ConfigID, b.DeviceID).First(&cfg).Error; err != nil {
			return err
		}
		plain, err := m.keys.OpenConfig(b.DeviceID, cfg.ID, cfg.ServerID, cfg.ServerRevisionID, cfg.ConfigText)
		if err != nil {
			return err
		}
		if fmt.Sprintf("%x", sha256.Sum256([]byte(plain))) != row.ConfigSHA256 {
			return ErrConflict
		}
		out = Configuration{ConfigID: cfg.ID, Text: plain, SHA256: row.ConfigSHA256}
		return nil
	})
	if err != nil {
		return Configuration{}, err
	}
	return out, nil
}

// Fixed result codes prevent accidental configuration/token disclosure in logs.
func validResult(success bool, code string) bool {
	if success {
		return code == "ok"
	}
	switch code {
	case "config_invalid", "start_failed", "stop_failed", "reload_failed", "diagnostic_failed", "internal_error":
		return true
	}
	return false
}
func (m *Manager) Finish(ctx context.Context, key, id, lease string, success bool, code string) error {
	return m.FinishWithReport(ctx, key, id, lease, success, code, nil)
}
func (m *Manager) FinishWithReport(ctx context.Context, key, id, lease string, success bool, code string, report *agentstatus.Report) error {
	data, err := agentstatus.Encode(report)
	if err != nil || !validResult(success, code) || (report != nil && (success != (report.State == "registered") || (!success && code != "diagnostic_failed"))) {
		return ErrInput
	}
	return m.auth.WithCredential(ctx, key, func(tx *gorm.DB, b model.AgentBinding) error {
		row, err := lockedCommand(tx, b, id, lease)
		if err != nil {
			return err
		}
		state := "failed"
		if success {
			state = "succeeded"
		}
		if row.Status == state && row.ResultCode == code && row.DiagnosticData == data {
			return nil
		}
		if report != nil {
			if row.Action != "diagnose" {
				return ErrInput
			}
			if report.ConfigID > 0 {
				var count int64
				if err := tx.Model(&model.FrpConfig{}).Where("id=? AND device_id=?", report.ConfigID, b.DeviceID).Count(&count).Error; err != nil {
					return err
				}
				if count != 1 {
					return ErrInput
				}
			}
		}
		now, err := nowDB(tx)
		if err != nil {
			return err
		}
		if !active(row, now) {
			return ErrConflict
		}
		if err := tx.Model(&row).Updates(map[string]any{"status": state, "result_code": code, "diagnostic_data": data, "completed_at": utc(now)}).Error; err != nil {
			return err
		}
		if success && row.Action == "forget_config" {
			if err := frpreservation.Release(tx, b.DeviceID, credential(b)); err != nil {
				return err
			}
		}
		return audit(tx, nil, row.ID, state, "")
	}, frpreservation.Lock)
}

func (m *Manager) List(ctx context.Context, actor int64, admin bool, device int64, limit int) ([]model.AgentCommand, error) {
	if device <= 0 || limit < 1 || limit > 100 {
		return nil, ErrInput
	}
	rows := make([]model.AgentCommand, 0)
	err := m.auth.WithManagedDevice(ctx, actor, admin, device, func(tx *gorm.DB, b model.AgentBinding) error {
		now, err := nowDB(tx)
		if err != nil {
			return err
		}
		if err := tx.Where("device_id=?", device).Order("created_at DESC,id DESC").Limit(limit).Find(&rows).Error; err != nil {
			return err
		}
		for i := range rows {
			r := &rows[i]
			report, err := agentstatus.Decode(r.DiagnosticData)
			if err != nil {
				return ErrConflict
			}
			r.Diagnostic = report
			if !live(r.Status) {
				continue
			}
			if r.CredentialID != credential(b) {
				r.Status = "cancelled"
			} else if !r.ExpiresAt.After(now) || (r.Status == "leased" && r.LeaseExpires != nil && !r.LeaseExpires.After(now) && r.Attempts >= MaxAttempts) {
				r.Status = "timed_out"
			}
		}
		return m.attachDiagnosticProxies(tx, device, rows)
	})
	if err != nil {
		return nil, err
	}
	return rows, nil
}
