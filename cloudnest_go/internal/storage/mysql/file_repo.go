package mysql

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"cloudenest_go/internal/blob"
	"cloudenest_go/internal/quota"
	"cloudenest_go/internal/service/contract"
	"cloudenest_go/internal/transfer"
	"cloudenest_go/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// FileRepo 是 FileRepository 的 MySQL 实现。
type FileRepo struct {
	db *gorm.DB
}

func NewFileRepo(db *gorm.DB) *FileRepo {
	return &FileRepo{db: db}
}

func (r *FileRepo) SearchFiles(ctx context.Context, uid int64, q contract.FileSearchQuery) ([]model.File, int64, error) {
	q.Name = strings.TrimSpace(q.Name)
	q.FileType = strings.ToLower(strings.TrimSpace(q.FileType))
	q.StartTime = strings.TrimSpace(q.StartTime)
	q.EndTime = strings.TrimSpace(q.EndTime)
	if q.Page <= 0 {
		q.Page = 1
	}
	if q.Limit <= 0 || q.Limit > 200 {
		q.Limit = 50
	}

	dbq := r.db.WithContext(ctx).
		Table("file f").
		Select("f.*").
		Joins("LEFT JOIN file_permission fp ON fp.file_id = f.id AND fp.user_id = ?", uid).
		Where("f.is_deleted = 0").
		Where("f.user_id = ? OR fp.permission IN ?", uid, []string{"read", "write"})

	if q.ParentID > 0 {
		dbq = dbq.Where("f.parent_id = ?", q.ParentID)
	}
	if q.Name != "" {
		dbq = dbq.Where("f.filename LIKE ?", "%"+q.Name+"%")
	}
	if q.FileType != "" {
		dbq = dbq.Where("LOWER(f.file_type) = ?", q.FileType)
	}
	if q.StartTime != "" {
		startAt, err := parseFlexibleTimeForFileRepo(q.StartTime)
		if err != nil {
			return nil, 0, fmt.Errorf("invalid start_time")
		}
		dbq = dbq.Where("f.update_time >= ?", startAt)
	}
	if q.EndTime != "" {
		endAt, err := parseFlexibleTimeForFileRepo(q.EndTime)
		if err != nil {
			return nil, 0, fmt.Errorf("invalid end_time")
		}
		dbq = dbq.Where("f.update_time <= ?", endAt)
	}

	var total int64
	if err := r.db.WithContext(ctx).Table("(?) as t", dbq).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	offset := (q.Page - 1) * q.Limit
	var rows []model.File
	if err := dbq.Order("f.is_folder DESC, f.filename ASC").Limit(q.Limit).Offset(offset).Find(&rows).Error; err != nil {
		return nil, 0, err
	}

	return rows, total, nil
}

func (r *FileRepo) ListFiles(ctx context.Context, uid int64, parentID int64) ([]model.File, error) {
	var rows []model.File
	err := r.db.WithContext(ctx).
		Table("file f").
		Select("f.*").
		Joins("LEFT JOIN file_permission fp ON fp.file_id = f.id AND fp.user_id = ?", uid).
		Where("f.parent_id = ? AND f.is_deleted = 0", parentID).
		Where("f.user_id = ? OR fp.permission IN ?", uid, []string{"read", "write"}).
		Order("f.is_folder DESC, f.filename ASC").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *FileRepo) ListRecycleBin(ctx context.Context, uid int64) ([]model.File, error) {
	var rows []model.File
	if err := r.db.WithContext(ctx).Where("user_id = ? AND is_deleted = 1", uid).Order("update_time DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *FileRepo) CanAccessFile(ctx context.Context, userID int64, fileID int64, requireWrite bool) (bool, error) {
	return canAccessFileForFileRepo(ctx, r.db, userID, fileID, requireWrite)
}

func (r *FileRepo) GetActiveFileByID(ctx context.Context, fileID int64) (model.File, error) {
	var f model.File
	if err := r.db.WithContext(ctx).First(&f, "id = ? AND is_deleted = 0", fileID).Error; err != nil {
		return model.File{}, err
	}
	return f, nil
}

func (r *FileRepo) ResolveFileVersion(ctx context.Context, uid, fileID int64, version int) (model.File, model.Storage, error) {
	if uid <= 0 {
		return model.File{}, model.Storage{}, contract.ErrForbidden
	}
	if fileID <= 0 || version <= 0 {
		return model.File{}, model.Storage{}, contract.ErrInvalidInput
	}
	var file model.File
	var storage model.Storage
	err := fileReadSnapshot(ctx, r.db, func(tx *gorm.DB) error {
		if err := tx.First(&file, "id=? AND is_deleted=0", fileID).Error; err != nil {
			return err
		}
		if err := requireTreeRead(tx, uid, []fileTreeNode{{File: file}}); err != nil {
			return err
		}
		var historical model.FileVersion
		if err := tx.Where("file_id=? AND version=?", fileID, version).First(&historical).Error; err != nil {
			return err
		}
		if err := requireHistoricalSource(tx, uid, file, historical, false); err != nil {
			return err
		}
		if err := tx.First(&storage, historical.StorageID).Error; err != nil {
			return err
		}
		if historical.Size == nil || storage.FileSize == nil || *historical.Size != *storage.FileSize {
			return contract.ErrConflict
		}
		return nil
	})
	return file, storage, err
}

func (r *FileRepo) GetStorageByID(ctx context.Context, storageID int64) (model.Storage, error) {
	var storage model.Storage
	if err := r.db.WithContext(ctx).First(&storage, "id = ?", storageID).Error; err != nil {
		return model.Storage{}, err
	}
	return storage, nil
}

func (r *FileRepo) GetSystemSettingByKey(ctx context.Context, key string) (model.SystemSetting, error) {
	var row model.SystemSetting
	if err := r.db.WithContext(ctx).Where("setting_key = ?", key).First(&row).Error; err != nil {
		return model.SystemSetting{}, err
	}
	return row, nil
}

// GetTransferRateSettings shares the existing settings policy lock. No DB
// connection/lock remains held during IO or limiter waits.
func (r *FileRepo) GetTransferRateSettings(ctx context.Context, uid int64) ([]model.SystemSetting, error) {
	ctx, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	var rows []model.SystemSetting
	keys := []string{"upload_speed_limit_kbps", "download_speed_limit_kbps", "upload_peak_speed_limit_kbps", "download_peak_speed_limit_kbps", "peak_window_enabled", "peak_window_start", "peak_window_end"}
	if uid > 0 {
		suffix := fmt.Sprintf("_user_%d", uid)
		for _, key := range []string{"peak_window_enabled", "peak_window_start", "peak_window_end"} {
			keys = append(keys, key+suffix)
		}
	}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := quota.LockPolicy(tx, false); err != nil {
			return err
		}
		return tx.Where("setting_key IN ?", keys).Find(&rows).Error
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	return rows, err
}

func (r *FileRepo) CreateFolderRecord(ctx context.Context, uid int64, name string, parentID int64) (model.File, error) {
	var folder model.File
	err := fileMetadataTransaction(ctx, r.db, func(tx *gorm.DB) error {
		if err := lockFileNamespaces(tx, fileNamespaceKey(uid, parentID)); err != nil {
			return err
		}
		if err := requireWriteParent(tx, uid, parentID); err != nil {
			return err
		}
		finalName, err := allocateFileName(tx, uid, parentID, name, true, 0, false)
		if err != nil {
			return err
		}
		folder = model.File{UserID: uid, Filename: finalName, ParentID: parentID, IsFolder: 1}
		return tx.Create(&folder).Error
	})
	return folder, err
}

func (r *FileRepo) RenameFile(ctx context.Context, uid, fileID int64, name string) (model.File, error) {
	var file model.File
	err := fileMetadataTransaction(ctx, r.db, func(tx *gorm.DB) error {
		var err error
		file, err = lockFileWithNamespace(tx, fileID, 0, false)
		if err != nil {
			return err
		}
		if err := requireFilePermission(tx, uid, file, true); err != nil {
			return err
		}
		finalName, err := allocateFileName(tx, file.UserID, file.ParentID, name, file.IsFolder == 1, file.ID, false)
		if err != nil {
			return err
		}
		if err := tx.Model(&file).Update("filename", finalName).Error; err != nil {
			return err
		}
		file.Filename = finalName
		return nil
	})
	return file, err
}

func (r *FileRepo) CountActiveChildren(ctx context.Context, parentID int64) (int64, error) {
	var childCount int64
	err := r.db.WithContext(ctx).
		Model(&model.File{}).
		Where("parent_id = ? AND is_deleted = 0", parentID).
		Count(&childCount).Error
	if err != nil {
		return 0, err
	}
	return childCount, nil
}

func (r *FileRepo) SoftDeleteFileWithLivePhotoCleanup(ctx context.Context, uid, fileID int64) error {
	return fileMetadataTransaction(ctx, r.db, func(tx *gorm.DB) error {
		file, err := lockFileWithNamespace(tx, fileID, 0, true)
		if err != nil {
			return err
		}
		if err := requireFilePermission(tx, uid, file, true); err != nil {
			return err
		}
		if file.IsFolder == 1 {
			var count int64
			if err := tx.Model(&model.File{}).Where("parent_id = ? AND is_deleted = 0", fileID).Count(&count).Error; err != nil {
				return err
			}
			if count > 0 {
				return contract.ErrFolderNotEmpty
			}
		}
		if err := tx.Model(&file).Update("is_deleted", 1).Error; err != nil {
			return err
		}
		return tx.Where("image_file_id = ? OR video_file_id = ?", fileID, fileID).Delete(&model.LivePhoto{}).Error
	})
}

func (r *FileRepo) MoveFiles(ctx context.Context, uid int64, fileIDs []int64, targetParentID int64) ([]int64, error) {
	var updated []int64
	err := fileMetadataTransaction(ctx, r.db, func(tx *gorm.DB) error {
		updated = nil
		nodes, err := lockFileTree(tx, fileIDs, 0, true, fileNamespaceKey(uid, targetParentID))
		if err != nil {
			return err
		}
		if err := requireWriteParent(tx, uid, targetParentID); err != nil {
			return err
		}
		roots := make(map[int64]bool)
		for _, id := range fileIDs {
			roots[id] = true
		}
		for _, node := range nodes {
			if err := requireFilePermission(tx, uid, node.File, true); err != nil {
				return err
			}
			if node.File.ID == targetParentID {
				return contract.ErrCannotMoveToDesc
			}
		}
		for _, node := range nodes {
			src := node.File
			if !roots[src.ID] {
				continue
			}
			if targetParentID == 0 && src.UserID != uid {
				return contract.ErrForbidden
			}
			if src.ID == targetParentID {
				return contract.ErrCannotMoveToItself
			}
			if src.IsFolder == 1 && targetParentID > 0 {
				descendant, err := isDescendantFolderForFileRepo(ctx, tx, targetParentID, src.ID)
				if err != nil {
					return err
				}
				if descendant {
					return contract.ErrCannotMoveToDesc
				}
			}
			if src.ParentID != targetParentID {
				finalName, err := allocateFileName(tx, src.UserID, targetParentID, src.Filename, src.IsFolder == 1, src.ID, true)
				if err != nil {
					return err
				}
				if err := tx.Model(&src).Updates(map[string]any{"parent_id": targetParentID, "filename": finalName}).Error; err != nil {
					return err
				}
			}
			updated = append(updated, src.ID)
		}
		return nil
	})
	return updated, err
}

func (r *FileRepo) CopyFiles(ctx context.Context, uid int64, fileIDs []int64, targetParentID int64) ([]int64, error) {
	var newIDs []int64
	err := fileMetadataTransaction(ctx, r.db, func(tx *gorm.DB) error {
		if err := quota.Begin(tx, uid); err != nil {
			return err
		}
		newIDs = nil
		nodes, err := lockFileTree(tx, fileIDs, 0, false, fileNamespaceKey(uid, targetParentID))
		if err != nil {
			return err
		}
		for _, node := range nodes {
			if err := requireFilePermission(tx, uid, node.File, false); err != nil {
				return err
			}
			if node.File.ID == targetParentID {
				return contract.ErrCannotMoveToDesc
			}
		}
		if err := requireWriteParent(tx, uid, targetParentID); err != nil {
			return err
		}
		traversal := copyTraversal{remaining: 10000, path: map[int64]bool{}}
		for _, id := range uniqueInt64ForFileRepo(fileIDs) {
			if id <= 0 {
				return contract.ErrInvalidInput
			}
			var src model.File
			if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).First(&src, "id = ? AND is_deleted = 0", id).Error; err != nil {
				return err
			}
			if err := requireFilePermission(tx, uid, src, false); err != nil {
				return err
			}
			if src.ID == targetParentID {
				return contract.ErrCannotMoveToItself
			}
			if src.IsFolder == 1 && targetParentID > 0 {
				descendant, err := isDescendantFolderForFileRepo(ctx, tx, targetParentID, src.ID)
				if err != nil {
					return err
				}
				if descendant {
					return contract.ErrCannotMoveToDesc
				}
			}
			copiedID, err := copyNodeForFileRepo(tx, uid, src, targetParentID, &traversal)
			if err != nil {
				return err
			}
			newIDs = append(newIDs, copiedID)
		}
		return quota.Refresh(tx, uid, true)
	})
	return newIDs, err
}

func (r *FileRepo) RestoreFromRecycleBin(ctx context.Context, uid, fileID int64) (model.File, error) {
	var restored model.File
	err := fileMetadataTransaction(ctx, r.db, func(tx *gorm.DB) error {
		var err error
		restored, err = lockFileWithNamespace(tx, fileID, 1, false)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return contract.ErrRecycleItemNotFound
		}
		if err != nil {
			return err
		}
		if restored.UserID != uid {
			return contract.ErrForbidden
		}
		if err := requireWriteParent(tx, uid, restored.ParentID); err != nil {
			return err
		}
		finalName, err := allocateFileName(tx, restored.UserID, restored.ParentID, restored.Filename, restored.IsFolder == 1, restored.ID, true)
		if err != nil {
			return err
		}
		if err := tx.Model(&restored).Updates(map[string]any{"is_deleted": 0, "filename": finalName}).Error; err != nil {
			return err
		}
		restored.Filename, restored.IsDeleted = finalName, 0
		return nil
	})
	return restored, err
}

func (r *FileRepo) PermanentDeleteFromRecycleBin(ctx context.Context, uid int64, fileID int64, clientIP string) error {
	return fileMetadataTransaction(ctx, r.db, func(tx *gorm.DB) error {
		if err := quota.Begin(tx, uid); err != nil {
			return err
		}
		nodes, err := lockFileTree(tx, []int64{fileID}, 1, true)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return contract.ErrRecycleItemNotFound
		}
		if err != nil {
			return err
		}
		ids := make([]int64, 0, len(nodes))
		// Ownership is required even when a write grant exists.
		for _, node := range nodes {
			if node.File.UserID != uid {
				return contract.ErrForbidden
			}
		}
		for _, node := range nodes {
			if node.File.IsDeleted != 1 {
				return contract.ErrConflict
			}
			ids = append(ids, node.File.ID)
		}

		if err := tx.Where("image_file_id IN ? OR video_file_id IN ?", ids, ids).Delete(&model.LivePhoto{}).Error; err != nil {
			return err
		}

		// 先清理依赖 file_id 的子表，避免 file 主表删除时触发外键约束失败。
		if err := tx.Where("file_id IN ?", ids).Delete(&model.AiTask{}).Error; err != nil {
			return err
		}
		if err := tx.Where("file_id IN ?", ids).Delete(&model.AiMetadata{}).Error; err != nil {
			return err
		}
		if err := tx.Where("file_id IN ?", ids).Delete(&model.FilePermission{}).Error; err != nil {
			return err
		}
		if err := tx.Where("file_id IN ?", ids).Delete(&model.FileVersion{}).Error; err != nil {
			return err
		}
		if err := tx.Where("file_id IN ?", ids).Delete(&model.Share{}).Error; err != nil {
			return err
		}
		// Audit history survives metadata deletion; record the operation atomically.
		if err := tx.Create(&model.OperationLog{UserID: &uid, FileID: &fileID, Action: "permanent_delete", Status: "success", Detail: fmt.Sprintf("deleted metadata nodes=%d", len(ids)), ClientIP: strings.TrimSpace(clientIP)}).Error; err != nil {
			return err
		}

		if err := tx.Where("id IN ?", ids).Delete(&model.File{}).Error; err != nil {
			return err
		}
		return quota.Refresh(tx, uid, false)
	})
}

func (r *FileRepo) GetUploadTaskByUploadID(ctx context.Context, uid int64, uploadID string) (model.UploadTask, error) {
	var task model.UploadTask
	if err := r.db.WithContext(ctx).Where("upload_id = ?", uploadID).First(&task).Error; err != nil {
		return model.UploadTask{}, err
	}
	if task.UserID == nil || *task.UserID != uid {
		return model.UploadTask{}, contract.ErrForbidden
	}
	if task.ContractVersion != 1 {
		return model.UploadTask{}, contract.ErrUploadContract
	}
	if task.Status != 1 && (task.ChunkProtocol != 1 || task.TransferProtocol != 1) {
		return model.UploadTask{}, contract.ErrUploadContract
	}
	if task.Status != 1 && task.TotalChunks != nil && (*task.TotalChunks < 0 || *task.TotalChunks > contract.MaxUploadChunks) {
		return model.UploadTask{}, contract.ErrUploadContract
	}
	if task.Status == 0 {
		if err := quota.RequireLiveTask(r.db.WithContext(ctx), task); err != nil {
			return model.UploadTask{}, err
		}
	}
	return task, nil
}

func (r *FileRepo) GetUploadTaskAndUploadedChunkIndexes(ctx context.Context, userID int64, uploadID string) (model.UploadTask, []int, error) {
	task, err := r.GetUploadTaskByUploadID(ctx, userID, uploadID)
	if err != nil {
		if errors.Is(err, contract.ErrForbidden) {
			err = gorm.ErrRecordNotFound
		}
		return model.UploadTask{}, nil, err
	}
	if task.UserID == nil || *task.UserID != userID {
		return model.UploadTask{}, nil, gorm.ErrRecordNotFound
	}

	var rows []model.UploadChunk
	err = r.db.WithContext(ctx).
		Where("upload_id = ? AND status = 1", uploadID).
		Order("chunk_index ASC").
		Limit(contract.MaxUploadChunks + 1).Find(&rows).Error
	if err != nil {
		return model.UploadTask{}, nil, err
	}

	chunkIndexes := make([]int, 0, len(rows))
	for _, row := range rows {
		chunkIndexes = append(chunkIndexes, row.ChunkIndex)
	}

	return task, chunkIndexes, nil
}

func (r *FileRepo) CreateUploadTask(ctx context.Context, task model.UploadTask) error {
	if task.UserID == nil || task.TotalSize == nil || task.ReservedBytes != *task.TotalSize || task.ReservedBytes <= 0 || task.ContractVersion != 1 {
		return contract.ErrInvalidInput
	}
	uid := *task.UserID
	return fileMetadataTransaction(ctx, r.db, func(tx *gorm.DB) error {
		if err := quota.Begin(tx, uid); err != nil {
			return err
		}
		expired := tx.Model(&model.UploadTask{}).Where("user_id=? AND contract_version=1 AND status IN (0,2) AND expires_at<=UTC_TIMESTAMP(6)", uid).Updates(map[string]any{"status": 4, "reserved_bytes": int64(0)})
		if expired.Error != nil {
			return expired.Error
		}
		if expired.RowsAffected > 0 {
			if err := tx.Create(&model.OperationLog{UserID: &uid, Action: "upload_expire", Status: "success", Detail: fmt.Sprintf("expired reservations=%d; physical cleanup pending", expired.RowsAffected)}).Error; err != nil {
				return err
			}
		}
		if err := lockFileNamespaces(tx, fileNamespaceKey(uid, task.ParentID)); err != nil {
			return err
		}
		if err := requireWriteParent(tx, uid, task.ParentID); err != nil {
			return err
		}
		attempt := task
		attempt.ID = 0
		if err := tx.Create(&attempt).Error; err != nil {
			return err
		}
		if err := tx.Model(&attempt).Update("expires_at", gorm.Expr("DATE_ADD(UTC_TIMESTAMP(6),INTERVAL 24 HOUR)")).Error; err != nil {
			return err
		}
		if err := quota.Refresh(tx, uid, true); err != nil {
			return err
		}
		return tx.Create(&model.OperationLog{UserID: &uid, Action: "upload_reserve", Status: "success", Detail: fmt.Sprintf("upload_id=%s bytes=%d", task.UploadID, task.ReservedBytes)}).Error
	})
}

func (r *FileRepo) UpsertUploadChunkAndRefreshProgress(ctx context.Context, taskID int64, uploadID string, chunkIndex int, chunkSize int64, chunkPath, chunkRoot, sha256, md5, artifactToken string) (contract.ChunkCommit, error) {
	var owner model.UploadTask
	if err := r.db.WithContext(ctx).First(&owner, taskID).Error; err != nil {
		return contract.ChunkCommit{CandidateDisposable: true}, err
	}
	if owner.UserID == nil {
		return contract.ChunkCommit{CandidateDisposable: true}, contract.ErrForbidden
	}
	prepared, unused := false, false
	err := fileMetadataTransaction(ctx, r.db, func(tx *gorm.DB) error {
		// A deadlock retry starts a fresh attempt, never inheriting commit evidence.
		prepared, unused = false, false
		if err := quota.Begin(tx, *owner.UserID); err != nil {
			return err
		}
		if err := transfer.CheckVolume(tx, artifactToken); err != nil {
			return err
		}
		var current model.UploadTask
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, "id=? AND upload_id=?", taskID, uploadID).Error; err != nil {
			return err
		}
		if current.UserID == nil || *current.UserID != *owner.UserID {
			return contract.ErrForbidden
		}
		if err := quota.RequireLiveTask(tx, current); err != nil {
			return err
		}
		if current.MergeArtifact != "" {
			return contract.ErrTransferBusy
		}
		expected, err := contract.ExpectedChunkBytes(current, chunkIndex)
		if err != nil {
			return err
		}
		if chunkSize != expected || !blob.ValidToken(sha256) || len(md5) != 32 {
			return contract.ErrInvalidInput
		}
		if err := blob.VerifyChunk(ctx, chunkRoot, chunkPath, expected, sha256); err != nil {
			return contract.ErrStorageIntegrity
		}
		if err := transfer.ValidateChunk(tx, artifactToken, current, chunkPath, chunkSize, sha256, true); err != nil {
			return err
		}
		var row model.UploadChunk
		err = tx.First(&row, "upload_id=? AND chunk_index=?", uploadID, chunkIndex).Error
		if err == nil {
			if row.Status != 1 || row.ChunkSize == nil || *row.ChunkSize != chunkSize || row.SHA256 != sha256 || row.MD5 != md5 {
				return contract.ErrConflict
			}
			if err := blob.VerifyChunk(ctx, chunkRoot, row.ChunkPath, expected, row.SHA256); err != nil {
				return contract.ErrStorageIntegrity
			}
			if err := transfer.ValidateChunk(tx, row.ArtifactToken, current, row.ChunkPath, chunkSize, sha256, false); err != nil {
				return err
			}
			unused = true
		} else {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			row = model.UploadChunk{ArtifactToken: artifactToken, UploadID: uploadID, ChunkIndex: chunkIndex, ChunkSize: &chunkSize, ChunkPath: chunkPath, SHA256: sha256, MD5: md5, Status: 1}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
			if err := transfer.AttachChunk(tx, artifactToken, current, chunkPath, chunkSize, sha256); err != nil {
				return err
			}
			var count int64
			if err := tx.Model(&model.UploadChunk{}).Where("upload_id=? AND status=1", uploadID).Count(&count).Error; err != nil {
				return err
			}
			if err := tx.Model(&model.UploadTask{}).Where("id=?", taskID).Update("uploaded_chunks", count).Error; err != nil {
				return err
			}
		}
		prepared = true
		return nil
	})
	if err != nil {
		return contract.ChunkCommit{CandidateDisposable: !prepared || unused}, err
	}
	return contract.ChunkCommit{CandidateDisposable: unused}, nil
}
func (r *FileRepo) ListDoneUploadChunks(ctx context.Context, uploadID string) ([]model.UploadChunk, error) {
	var rows []model.UploadChunk
	err := r.db.WithContext(ctx).Where("upload_id=? AND status=1", uploadID).Order("chunk_index ASC").Limit(contract.MaxUploadChunks + 1).Find(&rows).Error
	return rows, err
}

func (r *FileRepo) CompleteUploadPersist(ctx context.Context, uid int64, parentID int64, uploadID string, task model.UploadTask, totalChunks int, storagePath, storageRoot string, mergedSize int64, fileHash, artifactToken string, clientIP string) (contract.UploadCommit, error) {
	var createdFile model.File
	var canonical model.Storage
	var repaired bool
	var replayed bool
	prepared := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := quota.Begin(tx, uid); err != nil {
			return err
		}
		if err := transfer.CheckVolume(tx, artifactToken); err != nil {
			return err
		}
		if err := lockFileNamespaces(tx, fileNamespaceKey(uid, parentID)); err != nil {
			return err
		}
		var currentTask model.UploadTask
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&currentTask, "id=? AND upload_id=?", task.ID, uploadID).Error; err != nil {
			return err
		}
		if currentTask.UserID == nil || *currentTask.UserID != uid {
			return contract.ErrForbidden
		}
		if currentTask.Status == 1 {
			var err error
			createdFile, err = contract.UploadReceipt(currentTask, uid, parentID)
			replayed = err == nil
			return err
		}
		if err := quota.RequireLiveTask(tx, currentTask); err != nil {
			return err
		}
		if _, err := contract.ExpectedChunkBytes(currentTask, 0); err != nil {
			return err
		}
		if currentTask.ParentID != parentID {
			return contract.ErrUploadContract
		}
		if currentTask.Status != 0 {
			return contract.ErrConflict
		}
		if currentTask.TotalSize == nil || *currentTask.TotalSize != mergedSize || currentTask.TotalChunks == nil || *currentTask.TotalChunks != totalChunks {
			return contract.ErrInvalidInput
		}
		task = currentTask
		if err := requireWriteParent(tx, uid, parentID); err != nil {
			return err
		}
		selected, wasRepaired, err := ensureStorageForFileRepo(tx, fileHash, storagePath, storageRoot, mergedSize)
		if err != nil {
			return err
		}

		finalName, err := allocateFileName(tx, uid, parentID, strings.TrimSpace(task.Filename), false, 0, true)
		if err != nil {
			return err
		}

		fileType := strings.TrimPrefix(strings.ToLower(filepath.Ext(finalName)), ".")
		createdFile = model.File{
			UserID:    uid,
			Filename:  finalName,
			ParentID:  parentID,
			IsFolder:  0,
			FileType:  fileType,
			Size:      mergedSize,
			StorageID: &selected.ID,
		}
		if err := tx.Create(&createdFile).Error; err != nil {
			return err
		}

		canonical, repaired = selected, wasRepaired
		if err := preserveCurrentVersion(tx, createdFile, &uid, "Initial upload"); err != nil {
			return err
		}
		if repaired {
			if err := tx.Create(&model.OperationLog{UserID: &uid, FileID: &createdFile.ID, Action: "storage_repair", Status: "success", Detail: fmt.Sprintf("storage_id=%d verified upload replaced unavailable or corrupt object", canonical.ID), ClientIP: clientIP}).Error; err != nil {
				return err
			}
		}
		if err := tx.Create(&model.OperationLog{UserID: &uid, FileID: &createdFile.ID, Action: "upload_complete", Status: "success", Detail: fmt.Sprintf("upload_id=%s", uploadID), ClientIP: clientIP}).Error; err != nil {
			return err
		}
		// Read DB defaults before persisting the exact original response. The
		// receipt is informational and does not retain file/storage rows.
		if err := tx.First(&createdFile, createdFile.ID).Error; err != nil {
			return err
		}
		payload, err := json.Marshal(createdFile)
		if err != nil || len(payload) > 8192 {
			return contract.ErrUploadReceipt
		}
		transition := tx.Model(&model.UploadTask{}).Where("id=? AND status=0 AND contract_version=1 AND reserved_bytes=? AND parent_id=? AND expires_at>UTC_TIMESTAMP(6)", task.ID, mergedSize, parentID).Updates(map[string]interface{}{
			"reserved_bytes": int64(0), "status": 1, "uploaded_chunks": totalChunks,
			"result_version": int16(1), "result_file_id": createdFile.ID, "result_json": string(payload),
		})
		if transition.Error != nil {
			return transition.Error
		}
		if transition.RowsAffected != 1 {
			return contract.ErrUploadContract
		}

		if err := transfer.Publish(tx, artifactToken, currentTask, storagePath, canonical.FilePath, mergedSize, fileHash); err != nil {
			return err
		}
		if err := tx.Where("upload_id = ?", uploadID).Delete(&model.UploadChunk{}).Error; err != nil {
			return err
		}

		if err := quota.Refresh(tx, uid, true); err != nil {
			return err
		}
		prepared = true
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return contract.UploadCommit{CandidateDisposable: !prepared}, normalizeFileNameConstraintError(err)
	}
	return contract.UploadCommit{File: createdFile, CandidateDisposable: replayed || canonical.FilePath != storagePath, Replayed: replayed}, nil
}

func (r *FileRepo) ResolveValidShare(ctx context.Context, code string) (model.Share, model.File, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return model.Share{}, model.File{}, gorm.ErrRecordNotFound
	}

	var share model.Share
	if err := r.db.WithContext(ctx).Where("share_code = ?", code).First(&share).Error; err != nil {
		return model.Share{}, model.File{}, err
	}
	if share.ExpireTime != nil && time.Now().After(*share.ExpireTime) {
		return model.Share{}, model.File{}, contract.ErrShareExpired
	}
	if share.FileID == nil {
		return model.Share{}, model.File{}, gorm.ErrRecordNotFound
	}

	var file model.File
	if err := r.db.WithContext(ctx).First(&file, "id = ? AND is_deleted = 0", *share.FileID).Error; err != nil {
		return model.Share{}, model.File{}, err
	}

	if share.UserID == nil {
		return model.Share{}, model.File{}, gorm.ErrRecordNotFound
	}
	var creator model.User
	if err := r.db.WithContext(ctx).First(&creator, "id = ? AND status = 1", *share.UserID).Error; err != nil {
		return model.Share{}, model.File{}, err
	}
	canRead, err := canAccessFileForFileRepo(ctx, r.db, *share.UserID, file.ID, false)
	if err != nil {
		return model.Share{}, model.File{}, err
	}
	if !canRead {
		return model.Share{}, model.File{}, gorm.ErrRecordNotFound
	}

	return share, file, nil
}

func (r *FileRepo) CreateShare(ctx context.Context, uid int64, fileID int64, shareCode string, expireAt *time.Time) (model.Share, error) {
	fileIDCopy := fileID
	uidCopy := uid
	share := model.Share{FileID: &fileIDCopy, UserID: &uidCopy, ShareCode: shareCode, ExpireTime: expireAt}
	if err := r.db.WithContext(ctx).Create(&share).Error; err != nil {
		return model.Share{}, err
	}
	return share, nil
}

func (r *FileRepo) CreateOperationLog(ctx context.Context, userID int64, fileID *int64, action string, status string, detail string, clientIP string) error {
	uid := userID
	row := model.OperationLog{UserID: &uid, FileID: fileID, Action: action, Status: status, Detail: detail, ClientIP: clientIP}
	return r.db.WithContext(ctx).Create(&row).Error
}

func (r *FileRepo) ListShares(ctx context.Context, uid int64) ([]model.Share, error) {
	var shares []model.Share
	if err := r.db.WithContext(ctx).Where("user_id = ?", uid).Order("create_time DESC").Find(&shares).Error; err != nil {
		return nil, err
	}
	return shares, nil
}

func (r *FileRepo) RevokeShare(ctx context.Context, uid int64, code string) error {
	res := r.db.WithContext(ctx).Where("share_code = ? AND user_id = ?", code, uid).Delete(&model.Share{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (r *FileRepo) ListOperationLogs(ctx context.Context, uid int64, isAdmin bool, q contract.FileOperationLogQuery) ([]model.OperationLog, int64, error) {
	dbq := r.db.WithContext(ctx).Model(&model.OperationLog{})
	if !(isAdmin && q.All == 1) {
		dbq = dbq.Where("user_id = ?", uid)
	}
	if q.Action != "" {
		dbq = dbq.Where("action = ?", q.Action)
	}
	if q.Status != "" {
		dbq = dbq.Where("status = ?", q.Status)
	}
	if q.StartTime != "" {
		startAt, err := parseFlexibleTimeForFileRepo(q.StartTime)
		if err != nil {
			return nil, 0, fmt.Errorf("invalid start_time")
		}
		dbq = dbq.Where("create_time >= ?", startAt)
	}
	if q.EndTime != "" {
		endAt, err := parseFlexibleTimeForFileRepo(q.EndTime)
		if err != nil {
			return nil, 0, fmt.Errorf("invalid end_time")
		}
		dbq = dbq.Where("create_time <= ?", endAt)
	}

	var total int64
	if err := dbq.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	offset := (q.Page - 1) * q.Limit
	var rows []model.OperationLog
	if err := dbq.Order("id DESC").Limit(q.Limit).Offset(offset).Find(&rows).Error; err != nil {
		return nil, 0, err
	}

	return rows, total, nil
}

func (r *FileRepo) ExportOperationLogs(ctx context.Context, uid int64, isAdmin bool, q contract.FileOperationLogQuery) ([]model.OperationLog, error) {
	dbq := r.db.WithContext(ctx).Model(&model.OperationLog{})
	if !(isAdmin && q.All == 1) {
		dbq = dbq.Where("user_id = ?", uid)
	}
	if q.Action != "" {
		dbq = dbq.Where("action = ?", q.Action)
	}
	if q.Status != "" {
		dbq = dbq.Where("status = ?", q.Status)
	}
	if q.StartTime != "" {
		startAt, err := parseFlexibleTimeForFileRepo(q.StartTime)
		if err != nil {
			return nil, fmt.Errorf("invalid start_time")
		}
		dbq = dbq.Where("create_time >= ?", startAt)
	}
	if q.EndTime != "" {
		endAt, err := parseFlexibleTimeForFileRepo(q.EndTime)
		if err != nil {
			return nil, fmt.Errorf("invalid end_time")
		}
		dbq = dbq.Where("create_time <= ?", endAt)
	}

	var rows []model.OperationLog
	if err := dbq.Order("id DESC").Limit(q.Limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *FileRepo) FindLivePhotoAutoBindCandidate(ctx context.Context, parentID int64, excludeID int64, baseName string, needVideo bool) (*model.File, error) {
	var siblingFiles []model.File
	if err := r.db.WithContext(ctx).
		Where("parent_id = ? AND is_deleted = 0 AND is_folder = 0 AND id <> ?", parentID, excludeID).
		Order("id ASC").
		Find(&siblingFiles).Error; err != nil {
		return nil, err
	}

	for i := range siblingFiles {
		s := siblingFiles[i]
		sBase := strings.TrimSpace(strings.TrimSuffix(s.Filename, filepath.Ext(s.Filename)))
		if !strings.EqualFold(sBase, baseName) {
			continue
		}
		sExt := strings.ToLower(strings.TrimPrefix(filepath.Ext(s.Filename), "."))
		if needVideo && isLivePhotoVideoExtForFileRepo(sExt) {
			return &s, nil
		}
		if !needVideo && isLivePhotoImageExtForFileRepo(sExt) {
			return &s, nil
		}
	}

	return nil, nil
}

func (r *FileRepo) CreateLivePhotoAutoBindIfAbsent(ctx context.Context, userID int64, imageID int64, videoID int64, clientIP string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var exists model.LivePhoto
		lookupErr := tx.Where("image_file_id = ? OR video_file_id = ?", imageID, videoID).First(&exists).Error
		if lookupErr == nil {
			return nil
		}
		if !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
			return lookupErr
		}

		row := model.LivePhoto{ImageFileID: imageID, VideoFileID: videoID}
		if err := tx.Create(&row).Error; err != nil {
			low := strings.ToLower(err.Error())
			if strings.Contains(low, "duplicate") || strings.Contains(low, "1062") {
				return nil
			}
			return err
		}

		detail := fmt.Sprintf("live_photo_id=%d image_file_id=%d video_file_id=%d", row.ID, row.ImageFileID, row.VideoFileID)
		if len(detail) > 500 {
			detail = detail[:500]
		}
		return tx.Create(&model.OperationLog{
			UserID:   &userID,
			FileID:   &row.ImageFileID,
			Action:   "live_photo_auto_bind",
			Status:   "success",
			Detail:   detail,
			ClientIP: strings.TrimSpace(clientIP),
		}).Error
	})
}

func parseFlexibleTimeForFileRepo(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	layouts := []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02"}
	for _, layout := range layouts {
		t, err := time.ParseInLocation(layout, raw, time.Local)
		if err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid time format")
}

func uniqueInt64ForFileRepo(input []int64) []int64 {
	seen := make(map[int64]bool, len(input))
	result := make([]int64, 0, len(input))
	for _, v := range input {
		if seen[v] {
			continue
		}
		seen[v] = true
		result = append(result, v)
	}
	return result
}

func canAccessFileForFileRepo(ctx context.Context, db *gorm.DB, userID int64, fileID int64, requireWrite bool) (bool, error) {
	var f model.File
	err := db.WithContext(ctx).First(&f, "id = ? AND is_deleted = 0", fileID).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return false, nil
		}
		return false, err
	}
	if f.UserID == userID {
		return true, nil
	}

	permissions := []string{"read", "write"}
	if requireWrite {
		permissions = []string{"write"}
	}

	var count int64
	err = db.WithContext(ctx).
		Model(&model.FilePermission{}).
		Where("file_id = ? AND user_id = ? AND permission IN ?", fileID, userID, permissions).
		Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func isLivePhotoImageExtForFileRepo(ext string) bool {
	images := map[string]bool{"jpg": true, "jpeg": true, "heic": true}
	return images[ext]
}

func isLivePhotoVideoExtForFileRepo(ext string) bool {
	videos := map[string]bool{"mov": true, "mp4": true, "m4v": true}
	return videos[ext]
}

func isDescendantFolderForFileRepo(ctx context.Context, db *gorm.DB, candidateID int64, ancestorID int64) (bool, error) {
	if candidateID <= 0 || ancestorID <= 0 {
		return false, nil
	}

	current := candidateID
	seen := map[int64]bool{}
	for current > 0 {
		if seen[current] || len(seen) >= 256 {
			return false, contract.ErrConflict
		}
		seen[current] = true
		if current == ancestorID {
			return true, nil
		}
		var f model.File
		err := db.WithContext(ctx).Select("id", "parent_id").First(&f, "id = ? AND is_deleted = 0", current).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return false, nil
			}
			return false, err
		}
		if f.ParentID == current {
			return false, contract.ErrConflict
		}
		current = f.ParentID
	}

	return false, nil
}

type copyTraversal struct {
	remaining int
	path      map[int64]bool
}

func copyNodeForFileRepo(tx *gorm.DB, newOwnerID int64, src model.File, targetParentID int64, traversal *copyTraversal) (int64, error) {
	if src.IsFolder != 0 && src.StorageID != nil {
		return 0, contract.ErrStorageIntegrity
	}
	if traversal.remaining <= 0 || len(traversal.path) >= 256 || traversal.path[src.ID] {
		return 0, contract.ErrConflict
	}
	traversal.remaining--
	traversal.path[src.ID] = true
	defer delete(traversal.path, src.ID)
	if err := requireFilePermission(tx, newOwnerID, src, false); err != nil {
		return 0, err
	}
	finalName, err := allocateFileName(tx, newOwnerID, targetParentID, src.Filename, src.IsFolder == 1, 0, true)
	if err != nil {
		return 0, err
	}

	newNode := model.File{
		UserID:         newOwnerID,
		Filename:       finalName,
		ParentID:       targetParentID,
		IsFolder:       src.IsFolder,
		FileType:       src.FileType,
		Size:           src.Size,
		StorageID:      src.StorageID,
		WorkspaceID:    src.WorkspaceID,
		CurrentVersion: 1,
		IsDeleted:      0,
	}
	if err := tx.Create(&newNode).Error; err != nil {
		return 0, err
	}

	if src.IsFolder == 0 {
		if err := preserveCurrentVersion(tx, newNode, &newOwnerID, "Copied current content"); err != nil {
			return 0, err
		}
	}
	if src.IsFolder == 1 {
		var children []model.File
		if err := tx.Where("parent_id = ? AND is_deleted = 0", src.ID).Order("id ASC").Find(&children).Error; err != nil {
			return 0, err
		}
		for _, child := range children {
			if _, err := copyNodeForFileRepo(tx, newOwnerID, child, newNode.ID, traversal); err != nil {
				return 0, err
			}
		}
	}

	return newNode.ID, nil
}

// The unique hash arbitrates simultaneous publications across namespaces. No
// filesystem deletion occurs inside a metadata transaction.
func ensureStorageForFileRepo(tx *gorm.DB, fileHash, storagePath, storageRoot string, size int64) (model.Storage, bool, error) {
	root, rel, err := blob.ManagedLocation(storageRoot, storagePath)
	if err != nil {
		return model.Storage{}, false, err
	}
	candidate := model.Storage{FileHash: fileHash, FilePath: storagePath, FileSize: &size, Managed: true, ManagedRoot: root, ManagedPath: rel}
	if err := verifyStorageObject(tx.Statement.Context, candidate); err != nil {
		return model.Storage{}, false, err
	}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&candidate).Error; err != nil {
		return model.Storage{}, false, err
	}
	var selected model.Storage
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("file_hash=?", fileHash).First(&selected).Error; err != nil {
		return selected, false, err
	}
	if selected.FileSize == nil || *selected.FileSize != size {
		return selected, false, contract.ErrConflict
	}
	if selected.State != blob.Active {
		return model.Storage{}, false, contract.ErrConflict
	}
	if selected.OrphanedAt != nil {
		if err := tx.Model(&selected).Update("orphaned_at", nil).Error; err != nil {
			return selected, false, err
		}
	}
	if selected.FilePath == storagePath {
		return selected, false, nil
	}
	if err := verifyStorageObject(tx.Statement.Context, selected); err != nil {
		if tx.Statement.Context.Err() != nil {
			return selected, false, tx.Statement.Context.Err()
		}
		// The candidate has the same verified hash and size. Repoint metadata only;
		// leave the old path intact for diagnosis and any existing readers.
		token, tokenErr := blob.RandomToken()
		if tokenErr != nil {
			return selected, false, tokenErr
		}
		if err := tx.Create(&model.StorageGarbage{Token: token, StorageID: selected.ID, Root: selected.ManagedRoot, OriginalPath: selected.FilePath, FileHash: selected.FileHash, FileSize: *selected.FileSize, Status: "retained"}).Error; err != nil {
			return selected, false, err
		}
		if err := tx.Model(&selected).Updates(map[string]any{"file_path": storagePath, "managed": true, "managed_root": root, "managed_path": rel}).Error; err != nil {
			return selected, false, err
		}
		selected.FilePath = storagePath
		selected.Managed = true
		selected.ManagedRoot = root
		selected.ManagedPath = rel
		return selected, true, nil
	}
	return selected, false, nil
}

var _ contract.FileRepository = (*FileRepo)(nil)

func (r *FileRepo) CancelUploadTask(ctx context.Context, uid int64, id, ip string) error {
	return fileMetadataTransaction(ctx, r.db, func(tx *gorm.DB) error {
		if err := quota.Begin(tx, uid); err != nil {
			return err
		}
		var task model.UploadTask
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("upload_id=?", id).First(&task).Error; err != nil {
			return err
		}
		if task.UserID == nil || *task.UserID != uid {
			return contract.ErrForbidden
		}
		if task.Status == 3 {
			return nil
		}
		if task.ContractVersion != 1 || (task.Status != 0 && task.Status != 2 && task.Status != 4) {
			return contract.ErrUploadContract
		}
		if err := tx.Model(&task).Updates(map[string]any{"status": 3, "reserved_bytes": int64(0)}).Error; err != nil {
			return err
		}
		if err := quota.Refresh(tx, uid, false); err != nil {
			return err
		}
		return tx.Create(&model.OperationLog{UserID: &uid, Action: "upload_cancel", Status: "success", Detail: fmt.Sprintf("upload_id=%s metadata canceled; physical cleanup pending", id), ClientIP: ip}).Error
	})
}
