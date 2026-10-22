// Package maintenance contains explicit, bounded operator jobs. It never runs
// from normal server startup and never adopts legacy files automatically.
package maintenance

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cloudenest_go/internal/blob"
	"cloudenest_go/internal/store"
	"cloudenest_go/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	quarantining = "quarantining"
	quarantined  = "quarantined"
	restoring    = "restoring"
	blocked      = "blocked"
	batchSize    = 64
)

var ErrGCPending = errors.New("storage GC retained blocked or unfinished objects; inspect the JSON report")

type GCFailure struct {
	StorageID int64  `json:"storage_id"`
	Stage     string `json:"stage"`
}
type GCReport struct {
	BlockedObjects      int64       `json:"blocked_objects"`
	Scanned             int         `json:"scanned"`
	Marked              int         `json:"marked"`
	Quarantined         int         `json:"quarantined"`
	Restored            int         `json:"restored"`
	Tombstoned          int         `json:"tombstoned"`
	Unlinked            int         `json:"unlinked"`
	ExpiredLeasesPruned int64       `json:"expired_leases_pruned"`
	Failures            []GCFailure `json:"failures"`
}
type collector struct {
	db        *gorm.DB
	root      *os.Root
	canonical string
	report    *GCReport
}

// RunStorageGC makes one bounded pass with two fixed 24-hour grace periods.
// It never scans the filesystem for candidates or selects unmanaged rows.
func RunStorageGC(ctx context.Context, db *gorm.DB, storageRoot string) (GCReport, error) {
	report := GCReport{Failures: []GCFailure{}}
	canonical, err := blob.CanonicalRoot(storageRoot)
	if err != nil {
		return report, errors.New("GC storage root is unavailable")
	}
	root, err := os.OpenRoot(canonical)
	if err != nil {
		return report, errors.New("GC storage root cannot be anchored")
	}
	defer root.Close()
	err = withGCLock(ctx, db, canonical, func(conn *gorm.DB) error {
		if err := store.CheckFileSchema(conn); err != nil {
			return err
		}
		if err := store.CheckStorageGCReferenceGraph(conn); err != nil {
			return err
		}
		c := collector{db: conn.Session(&gorm.Session{NewDB: true}), root: root, canonical: canonical, report: &report}
		// Finish committed tombstones first, even if their parent rows no longer
		// exist. A retained repair path is never part of this selection.
		var journals []model.StorageGarbage
		if err := conn.Where("root=? AND status='pending'", canonical).Order("created_at,token").Limit(batchSize).Find(&journals).Error; err != nil {
			return err
		}
		for _, j := range journals {
			if err := c.unlink(ctx, j.Token); err != nil {
				report.Failures = append(report.Failures, GCFailure{j.StorageID, "unlink_pending"})
			}
		}
		var ids []int64
		if err := conn.Model(&model.Storage{}).Where(`managed=1 AND managed_root=? AND (
   state IN ('quarantining','quarantined','restoring') OR
   (state='active' AND (orphaned_at IS NULL OR orphaned_at<=DATE_SUB(UTC_TIMESTAMP(6),INTERVAL 24 HOUR))
    AND NOT EXISTS (SELECT 1 FROM file f WHERE f.storage_id=storage.id)
    AND NOT EXISTS (SELECT 1 FROM file_version v WHERE v.storage_id=storage.id)
    AND NOT EXISTS (SELECT 1 FROM storage_read_lease l WHERE l.storage_id=storage.id AND l.expires_at>UTC_TIMESTAMP(6))))`, canonical).Order("id").Limit(batchSize).Pluck("id", &ids).Error; err != nil {
			return err
		}
		for _, id := range ids {
			if err := ctx.Err(); err != nil {
				return err
			}
			report.Scanned++
			if err := c.process(ctx, id); err != nil {
				report.Failures = append(report.Failures, GCFailure{id, "object_retained"})
			}
		}
		if err := conn.Model(&model.Storage{}).Where("managed=1 AND managed_root=? AND state=?", canonical, blocked).Count(&report.BlockedObjects).Error; err != nil {
			return err
		}
		if len(report.Failures) > 0 || report.BlockedObjects > 0 {
			return ErrGCPending
		}
		return nil
	})
	return report, err
}

func withGCLock(ctx context.Context, db *gorm.DB, root string, fn func(*gorm.DB) error) error {
	return db.WithContext(ctx).Connection(func(conn *gorm.DB) (result error) {
		conn = conn.Session(&gorm.Session{NewDB: true})
		var database string
		if err := conn.Raw("SELECT DATABASE()").Scan(&database).Error; err != nil {
			return err
		}
		digest := sha256.Sum256([]byte(database + "\x00" + root))
		name := fmt.Sprintf("cn:storage-gc:%x", digest[:16])
		var got sql.NullInt64
		discard := func() {
			if c, ok := conn.Statement.ConnPool.(*sql.Conn); ok {
				_ = c.Raw(func(any) error { return driver.ErrBadConn })
			}
		}
		if err := conn.Raw("SELECT GET_LOCK(?,10)", name).Scan(&got).Error; err != nil {
			discard()
			return err
		}
		if !got.Valid || got.Int64 != 1 {
			return errors.New("storage GC maintenance lock unavailable")
		}
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var released sql.NullInt64
			if err := conn.WithContext(ctx).Raw("SELECT RELEASE_LOCK(?)", name).Scan(&released).Error; err != nil || !released.Valid || released.Int64 != 1 {
				discard()
				result = errors.Join(result, errors.New("GC maintenance lock release failed"))
			}
		}()
		return fn(conn)
	})
}

func retained(tx *gorm.DB, id int64) (bool, error) {
	var n int64
	for _, query := range []string{
		"SELECT COUNT(*) FROM file WHERE storage_id=?",
		"SELECT COUNT(*) FROM file_version WHERE storage_id=?",
		"SELECT COUNT(*) FROM storage_read_lease WHERE storage_id=? AND expires_at>UTC_TIMESTAMP(6)",
	} {
		if err := tx.Raw(query, id).Scan(&n).Error; err != nil {
			return false, err
		}
		if n > 0 {
			return true, nil
		}
	}
	return false, nil
}
func audit(tx *gorm.DB, id int64, action string) error {
	return tx.Create(&model.OperationLog{Action: action, Status: "success", Detail: fmt.Sprintf("storage_id=%d", id)}).Error
}
func (c *collector) valid(s model.Storage) bool {
	return s.Managed && s.ManagedRoot == c.canonical && blob.CleanRelative(s.ManagedPath) && strings.HasPrefix(s.ManagedPath, ".managed-v1"+string(filepath.Separator)) && filepath.IsAbs(s.FilePath) && s.FilePath == filepath.Join(c.canonical, s.ManagedPath) && s.FileSize != nil && *s.FileSize >= 0 && len(s.FileHash) == 64
}
func validTrash(token, path string) bool {
	return blob.ValidToken(token) && path == filepath.Join(".gc", token+".blob")
}

func (c *collector) process(ctx context.Context, id int64) error {
	var s model.Storage
	var action string
	err := c.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&s, id).Error; err != nil {
			return err
		}
		if !s.Managed || s.ManagedRoot != c.canonical {
			return nil
		}
		if !c.valid(s) {
			if err := tx.Model(&s).Update("state", blocked).Error; err != nil {
				return err
			}
			if err := audit(tx, id, "storage_gc_blocked"); err != nil {
				return err
			}
			action = blocked
			return nil
		}
		keep, err := retained(tx, id)
		if err != nil {
			return err
		}
		if s.State == restoring || (keep && s.State != blob.Active) {
			if !validTrash(s.GCToken, s.GCPath) {
				return blob.ErrManagedPath
			}
			action = restoring
			return tx.Model(&s).Update("state", restoring).Error
		}
		if keep {
			return tx.Model(&s).Update("orphaned_at", nil).Error
		}
		switch s.State {
		case blob.Active:
			if s.OrphanedAt == nil {
				if err := tx.Model(&s).Update("orphaned_at", gorm.Expr("UTC_TIMESTAMP(6)")).Error; err != nil {
					return err
				}
				if err := audit(tx, id, "storage_gc_observe"); err != nil {
					return err
				}
				action = "observed"
				return nil
			}
			var oldEnough int64
			if err := tx.Raw("SELECT COUNT(*) FROM storage WHERE id=? AND orphaned_at<=DATE_SUB(UTC_TIMESTAMP(6),INTERVAL 24 HOUR)", id).Scan(&oldEnough).Error; err != nil {
				return err
			}
			if oldEnough == 0 {
				return nil
			}
			token, err := blob.RandomToken()
			if err != nil {
				return err
			}
			s.GCToken = token
			s.GCPath = filepath.Join(".gc", token+".blob")
			s.State = quarantining
			if err := tx.Model(&s).Updates(map[string]any{"state": s.State, "gc_token": s.GCToken, "gc_path": s.GCPath}).Error; err != nil {
				return err
			}
			if err := audit(tx, id, "storage_gc_mark"); err != nil {
				return err
			}
			action = quarantining
		case quarantining:
			action = quarantining
		case quarantined:
			action = quarantined
		default:
			return blob.ErrManagedPath
		}
		return nil
	})
	if err != nil {
		return err
	}
	switch action {
	case blocked:
		return ErrGCPending
	case "observed":
		c.report.Marked++
		return nil
	case "":
		return nil
	case restoring:
		return c.restore(ctx, s)
	case quarantining:
		if !validTrash(s.GCToken, s.GCPath) {
			return blob.ErrManagedPath
		}
		if err := c.move(ctx, s.ManagedPath, s.GCPath, s); err != nil {
			return c.block(ctx, s.ID)
		}
		err := c.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var current model.Storage
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, s.ID).Error; err != nil {
				return err
			}
			if current.State != quarantining || current.GCToken != s.GCToken {
				return ErrGCPending
			}
			if err := tx.Model(&current).Updates(map[string]any{"state": quarantined, "quarantined_at": gorm.Expr("UTC_TIMESTAMP(6)")}).Error; err != nil {
				return err
			}
			return audit(tx, s.ID, "storage_gc_quarantine")
		})
		if err != nil {
			return err
		}
		c.report.Quarantined++
		return nil
	case quarantined:
		return c.tombstone(ctx, s)
	}
	return nil
}

func (c *collector) block(ctx context.Context, id int64) error {
	// Files remain at whichever of the two known paths survived. Never try to
	// "repair" inconsistent filesystem state by deleting one of them.
	_ = c.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.Storage{}).Where("id=?", id).Update("state", blocked).Error; err != nil {
			return err
		}
		return audit(tx, id, "storage_gc_blocked")
	})
	return ErrGCPending
}

func (c *collector) restore(ctx context.Context, s model.Storage) error {
	if err := c.move(ctx, s.GCPath, s.ManagedPath, s); err != nil {
		return c.block(ctx, s.ID)
	}
	err := c.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current model.Storage
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, s.ID).Error; err != nil {
			return err
		}
		if current.State != restoring || current.GCToken != s.GCToken {
			return ErrGCPending
		}
		if err := tx.Model(&current).Updates(map[string]any{"state": blob.Active, "orphaned_at": nil, "quarantined_at": nil, "gc_token": "", "gc_path": ""}).Error; err != nil {
			return err
		}
		return audit(tx, s.ID, "storage_gc_restore")
	})
	if err == nil {
		c.report.Restored++
	}
	return err
}

func (c *collector) tombstone(ctx context.Context, s model.Storage) error {
	if !validTrash(s.GCToken, s.GCPath) {
		return blob.ErrManagedPath
	}
	if err := c.verify(ctx, s.GCPath, s); err != nil {
		return c.block(ctx, s.ID)
	}
	// A recreated original or a surviving hard link means quarantine was not
	// fully committed on disk. Retain it rather than interpreting it as garbage.
	if _, err := c.root.Lstat(s.ManagedPath); !errors.Is(err, os.ErrNotExist) {
		return c.block(ctx, s.ID)
	}
	committed := false
	var removedLeases int64
	err := c.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current model.Storage
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, s.ID).Error; err != nil {
			return err
		}
		if !c.valid(current) || current.State != quarantined || current.GCToken != s.GCToken {
			return ErrGCPending
		}
		keep, err := retained(tx, s.ID)
		if err != nil {
			return err
		}
		if keep {
			return ErrGCPending
		} // Next pass restores, never delete a new reference.
		var oldEnough int64
		if err := tx.Raw("SELECT COUNT(*) FROM storage WHERE id=? AND quarantined_at<=DATE_SUB(UTC_TIMESTAMP(6),INTERVAL 24 HOUR)", s.ID).Scan(&oldEnough).Error; err != nil {
			return err
		}
		if oldEnough == 0 {
			return nil
		}
		pruned := tx.Where("storage_id=? AND expires_at<=UTC_TIMESTAMP(6)", s.ID).Delete(&model.StorageReadLease{})
		if pruned.Error != nil {
			return pruned.Error
		}
		removedLeases = pruned.RowsAffected
		j := model.StorageGarbage{Token: s.GCToken, StorageID: s.ID, Root: c.canonical, OriginalPath: s.ManagedPath, TrashPath: s.GCPath, FileHash: s.FileHash, FileSize: *s.FileSize, Status: "pending"}
		if err := tx.Create(&j).Error; err != nil {
			return err
		}
		if err := audit(tx, s.ID, "storage_gc_tombstone"); err != nil {
			return err
		}
		result := tx.Delete(&current)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrGCPending
		}
		committed = true
		return nil
	})
	if err != nil {
		return err
	} // An uncertain commit never authorizes an unlink.
	if !committed {
		return nil
	}
	c.report.ExpiredLeasesPruned += removedLeases
	c.report.Tombstoned++
	return c.unlink(ctx, s.GCToken)
}

func (c *collector) unlink(ctx context.Context, token string) error {
	completed := false
	err := c.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var j model.StorageGarbage
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&j, "token=?", token).Error; err != nil {
			return err
		}
		if j.Status != "pending" {
			return nil
		}
		if j.Root != c.canonical || !validTrash(j.Token, j.TrashPath) || !blob.CleanRelative(j.OriginalPath) {
			return blob.ErrManagedPath
		}
		var count int64
		if err := tx.Model(&model.Storage{}).Where("id=? OR file_path IN ?", j.StorageID, []string{filepath.Join(c.canonical, j.TrashPath), filepath.Join(c.canonical, j.OriginalPath)}).Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return ErrGCPending
		}
		// Parent removal and its audit/journal are already durable before touching
		// bytes. If this transaction fails after Remove, the pending row can recover
		// from a missing trash path without resurrecting its deleted storage ID.
		s := model.Storage{FileHash: j.FileHash, FileSize: &j.FileSize}
		if err := c.verify(ctx, j.TrashPath, s); err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return err
			}
		} else {
			if err := c.root.Remove(j.TrashPath); err != nil {
				return err
			}
		}
		if err := c.syncDir(".gc"); err != nil {
			return err
		}
		if err := tx.Model(&j).Updates(map[string]any{"status": "done", "completed_at": gorm.Expr("UTC_TIMESTAMP(6)")}).Error; err != nil {
			return err
		}
		completed = true
		return nil
	})
	if err == nil && completed {
		c.report.Unlinked++
	}
	return err
}

// move never overwrites a destination. Link + directory sync + unlink recovers
// safely from either name existing, including a crash with both hard links.
func (c *collector) move(ctx context.Context, from, to string, s model.Storage) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fromInfo, fromErr := c.root.Lstat(from)
	toInfo, toErr := c.root.Lstat(to)
	if fromErr != nil && !errors.Is(fromErr, os.ErrNotExist) {
		return fromErr
	}
	if toErr != nil && !errors.Is(toErr, os.ErrNotExist) {
		return toErr
	}
	switch {
	case fromErr == nil && toErr == nil:
		if !os.SameFile(fromInfo, toInfo) {
			return blob.ErrManagedPath
		}
		if err := c.verify(ctx, from, s); err != nil {
			return err
		}
		if err := c.verify(ctx, to, s); err != nil {
			return err
		}
	case fromErr == nil && errors.Is(toErr, os.ErrNotExist):
		if err := c.verify(ctx, from, s); err != nil {
			return err
		}
		// Reject symlink parents of a destination before any mutation. Root still
		// protects the boundary if a privileged outside writer violates privacy.
		if err := blob.EnsurePrivateDir(c.root, filepath.Dir(to)); err != nil {
			return err
		}
		if err := c.root.Link(from, to); err != nil {
			return err
		}
		if err := c.syncParents(filepath.Dir(to)); err != nil {
			return err
		}
	case errors.Is(fromErr, os.ErrNotExist) && toErr == nil:
		if err := c.verify(ctx, to, s); err != nil {
			return err
		}
		if err := c.syncParents(filepath.Dir(to)); err != nil {
			return err
		}
		return c.syncDir(filepath.Dir(from))
	default:
		return blob.ErrManagedPath
	}
	if err := c.root.Remove(from); err != nil {
		return err
	}
	return c.syncDir(filepath.Dir(from))
}
func (c *collector) syncParents(path string) error {
	for {
		if err := c.syncDir(path); err != nil {
			return err
		}
		if path == "." {
			return nil
		}
		path = filepath.Dir(path)
	}
}
func (c *collector) syncDir(path string) error {
	dir, err := c.root.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}
func (c *collector) verify(ctx context.Context, path string, s model.Storage) error {
	if s.FileSize == nil || *s.FileSize < 0 || len(s.FileHash) != 64 {
		return blob.ErrManagedPath
	}
	if _, err := hex.DecodeString(s.FileHash); err != nil {
		return blob.ErrManagedPath
	}
	if err := blob.RegularPath(c.root, path); err != nil {
		return err
	}
	in, err := c.root.Open(path)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	if info.Size() != *s.FileSize {
		return blob.ErrManagedPath
	}
	hash := sha256.New()
	size, err := io.CopyBuffer(hash, &gcReader{ctx: ctx, in: io.LimitReader(in, *s.FileSize)}, make([]byte, 64*1024))
	if err != nil {
		return err
	}
	if size != *s.FileSize || hex.EncodeToString(hash.Sum(nil)) != strings.ToLower(s.FileHash) {
		return blob.ErrManagedPath
	}
	return nil
}

// PruneExpiredReadLeases is separate from physical GC and covers legacy objects
// as well. The DELETE rechecks expiry under its row locks; no unexpired token is
// removed, and a renewal cannot revive an already expired lease.
func PruneExpiredReadLeases(ctx context.Context, db *gorm.DB) (int64, error) {
	if err := store.CheckStorageLeaseSchema(db.WithContext(ctx)); err != nil {
		return 0, err
	}
	result := db.WithContext(ctx).Exec("DELETE FROM storage_read_lease WHERE expires_at<=UTC_TIMESTAMP(6) ORDER BY expires_at,token LIMIT 256")
	return result.RowsAffected, result.Error
}

type gcReader struct {
	ctx context.Context
	in  io.Reader
}

func (r *gcReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.in.Read(p)
}
