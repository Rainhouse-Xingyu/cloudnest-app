// Package indexqueue provides transactional outbox scheduling and fenced worker
// leases. It performs no model calls or file IO inside database transactions.
package indexqueue

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"math"

	"cloudenest_go/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	Pipeline     = "content-v1"
	MaxAttempts  = 5
	LeaseSeconds = 90
)

var (
	ErrLostLease     = errors.New("index task lease lost")
	ErrSourceChanged = errors.New("index source changed or unavailable")
	ErrInvalid       = errors.New("invalid index task")
)

type Queue struct{ db *gorm.DB }

func New(db *gorm.DB) *Queue { return &Queue{db: db} }

// Enqueue must run in the caller's transaction, after namespace/tree locks.
// Lock order is file -> job, shared by publishing and file mutations. Replaying
// an unchanged source is a no-op; force starts a new generation, even if running.
func Enqueue(tx *gorm.DB, fileID int64, force bool) error {
	var f model.File
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&f, "id=?", fileID).Error; err != nil {
		return err
	}
	hash, err := sourceHash(tx, f)
	if err != nil {
		return err
	}
	var job model.AIIndexJob
	err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&job, "file_id=?", fileID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return tx.Model(&model.AIIndexJob{}).Create(map[string]any{
			"file_id": fileID, "generation": int64(1), "source_hash": hash,
			"pipeline": Pipeline, "status": "queued", "attempts": 0,
			"available_at": gorm.Expr("UTC_TIMESTAMP(6)"), "lease_token": "", "error_code": "",
			"updated_at": gorm.Expr("UTC_TIMESTAMP(6)"),
		}).Error
	}
	if err != nil {
		return err
	}
	if !force && job.SourceHash == hash && job.Pipeline == Pipeline {
		return nil
	}
	if job.Generation <= 0 || job.Generation == math.MaxInt64 {
		return ErrInvalid
	}
	return tx.Model(&model.AIIndexJob{}).Where("file_id=?", fileID).Updates(map[string]any{
		"generation": job.Generation + 1, "source_hash": hash, "pipeline": Pipeline,
		"status": "queued", "attempts": 0, "available_at": gorm.Expr("UTC_TIMESTAMP(6)"),
		"lease_token": "", "lease_until": nil, "error_code": "", "updated_at": gorm.Expr("UTC_TIMESTAMP(6)"),
	}).Error
}

func sourceHash(tx *gorm.DB, f model.File) (string, error) {
	if f.IsDeleted != 0 || f.IsFolder != 0 || f.StorageID == nil {
		return "", ErrSourceChanged
	}
	var s model.Storage
	if err := tx.First(&s, *f.StorageID).Error; err != nil {
		return "", err
	}
	b, err := hex.DecodeString(s.FileHash)
	if err != nil || len(b) != 32 || hex.EncodeToString(b) != s.FileHash {
		return "", ErrSourceChanged
	}
	return s.FileHash, nil
}

// Claim uses MySQL's clock and SKIP LOCKED so independent processes can safely
// consume the same queue. A crashed worker consumes an attempt; the fifth expired
// claim becomes failed, rather than spinning forever. nil means no claim this poll.
func (q *Queue) Claim(ctx context.Context) (*model.AIIndexJob, error) {
	var result *model.AIIndexJob
	err := q.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job model.AIIndexJob
		err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where("pipeline=? AND ((status IN ('queued','retry') AND available_at<=UTC_TIMESTAMP(6)) OR (status='running' AND lease_until<=UTC_TIMESTAMP(6)))", Pipeline).
			Order("available_at, file_id").Take(&job).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if job.Attempts >= MaxAttempts {
			return tx.Model(&job).Updates(map[string]any{"status": "failed", "error_code": "attempts_exhausted", "lease_token": "", "lease_until": nil, "updated_at": gorm.Expr("UTC_TIMESTAMP(6)")}).Error
		}
		if job.Generation < 1 || job.Attempts < 0 {
			return ErrInvalid
		}
		var token [32]byte
		if _, err := rand.Read(token[:]); err != nil {
			return err
		}
		err = tx.Model(&job).Updates(map[string]any{
			"status": "running", "attempts": job.Attempts + 1,
			"lease_token": hex.EncodeToString(token[:]),
			"lease_until": gorm.Expr("TIMESTAMPADD(SECOND, ?, UTC_TIMESTAMP(6))", LeaseSeconds),
			"updated_at":  gorm.Expr("UTC_TIMESTAMP(6)"),
		}).Error
		if err != nil {
			return err
		}
		if err := tx.First(&job, "file_id=?", job.FileID).Error; err != nil {
			return err
		}
		result = &job
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func owned(db *gorm.DB, j model.AIIndexJob) *gorm.DB {
	return db.Model(&model.AIIndexJob{}).Where("file_id=? AND generation=? AND source_hash=? AND pipeline=? AND status='running' AND lease_token=? AND lease_token<>'' AND lease_until>UTC_TIMESTAMP(6)", j.FileID, j.Generation, j.SourceHash, j.Pipeline, j.LeaseToken)
}

func changed(result *gorm.DB) error {
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrLostLease
	}
	return nil
}

func (q *Queue) Renew(ctx context.Context, j model.AIIndexJob) error {
	return changed(owned(q.db.WithContext(ctx), j).Updates(map[string]any{
		"lease_until": gorm.Expr("TIMESTAMPADD(SECOND, ?, UTC_TIMESTAMP(6))", LeaseSeconds),
		"updated_at":  gorm.Expr("UTC_TIMESTAMP(6)"),
	}))
}

// Fail accepts only stable diagnostic codes, never raw model responses, file
// content, paths or credentials. retryable errors receive bounded backoff.
func (q *Queue) Fail(ctx context.Context, j model.AIIndexJob, code string, retryable bool) error {
	switch code {
	case "extraction_unavailable", "extraction_busy", "model_unavailable", "model_changed", "extraction_failed", "extraction_limit", "invalid_embedding", "source_changed", "unsupported_type", "worker_stopped", "index_write_failed", "source_integrity":
	default:
		return ErrInvalid
	}
	status := "failed"
	if retryable && j.Attempts < MaxAttempts {
		status = "retry"
	}
	if j.Attempts < 1 || j.Attempts > MaxAttempts {
		return ErrInvalid
	}
	delay := 5 << (j.Attempts - 1)
	return changed(owned(q.db.WithContext(ctx), j).Where("attempts=?", j.Attempts).Updates(map[string]any{
		"status": status, "error_code": code, "lease_token": "", "lease_until": nil,
		"available_at": gorm.Expr("TIMESTAMPADD(SECOND, ?, UTC_TIMESTAMP(6))", delay),
		"updated_at":   gorm.Expr("UTC_TIMESTAMP(6)"),
	}))
}

// Publish runs only short database writes in writeResult. Extraction, embedding
// and blob validation must already be finished. File state is checked under its
// mutation lock; completion is atomic with the result and rechecks lease expiry
// after writeResult, so a slow transaction cannot publish after losing its lease.
func (q *Queue) Publish(ctx context.Context, j model.AIIndexJob, writeResult func(*gorm.DB) error) error {
	if writeResult == nil {
		return ErrInvalid
	}
	return q.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var f model.File
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&f, "id=?", j.FileID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrSourceChanged
			}
			return err
		}
		hash, err := sourceHash(tx, f)
		if err != nil {
			return err
		}
		if hash != j.SourceHash {
			return ErrSourceChanged
		}
		var current model.AIIndexJob
		if err := owned(tx, j).Clauses(clause.Locking{Strength: "UPDATE"}).Take(&current).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrLostLease
			}
			return err
		}
		if err := writeResult(tx); err != nil {
			return err
		}
		return changed(owned(tx, j).Updates(map[string]any{
			"status": "done", "lease_token": "", "lease_until": nil, "error_code": "", "updated_at": gorm.Expr("UTC_TIMESTAMP(6)"),
		}))
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
}
