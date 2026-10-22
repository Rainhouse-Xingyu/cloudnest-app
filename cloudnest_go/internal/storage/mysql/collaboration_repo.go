package mysql

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"cloudenest_go/internal/quota"
	"cloudenest_go/internal/service/contract"
	"cloudenest_go/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CollaborationRepo 是协作模块仓储实现。
type CollaborationRepo struct {
	db *gorm.DB
}

func NewCollaborationRepo(db *gorm.DB) *CollaborationRepo {
	return &CollaborationRepo{db: db}
}

func (r *CollaborationRepo) CreateWorkspace(ctx context.Context, ws *model.Workspace, ownerID int64) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(ws).Error; err != nil {
			return err
		}
		row := model.WorkspaceUser{WorkspaceID: ws.ID, UserID: ownerID, Role: "owner"}
		return tx.Create(&row).Error
	})
}

func (r *CollaborationRepo) ListWorkspaces(ctx context.Context, uid int64) ([]contract.CollaborationWorkspaceListRow, error) {
	items := make([]contract.CollaborationWorkspaceListRow, 0)
	err := r.db.WithContext(ctx).
		Table("workspace w").
		Select("w.id, w.name, w.owner_id, wu.role, w.create_time").
		Joins("JOIN workspace_user wu ON wu.workspace_id = w.id AND wu.user_id = ?", uid).
		Order("w.id DESC").
		Find(&items).Error
	if err != nil {
		return nil, err
	}
	return items, nil
}

func (r *CollaborationRepo) GetWorkspaceMember(ctx context.Context, workspaceID int64, userID int64) (model.WorkspaceUser, error) {
	var row model.WorkspaceUser
	err := r.db.WithContext(ctx).Where("workspace_id = ? AND user_id = ?", workspaceID, userID).First(&row).Error
	if err != nil {
		return model.WorkspaceUser{}, err
	}
	return row, nil
}

func (r *CollaborationRepo) GetUserByID(ctx context.Context, userID int64) (model.User, error) {
	var user model.User
	err := r.db.WithContext(ctx).First(&user, "id = ?", userID).Error
	if err != nil {
		return model.User{}, err
	}
	return user, nil
}

func (r *CollaborationRepo) GetUserByUsername(ctx context.Context, username string) (model.User, error) {
	var user model.User
	err := r.db.WithContext(ctx).First(&user, "username = ?", username).Error
	if err != nil {
		return model.User{}, err
	}
	return user, nil
}

func (r *CollaborationRepo) UpsertWorkspaceMember(ctx context.Context, workspaceID int64, targetUserID int64, role string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row model.WorkspaceUser
		err := tx.Where("workspace_id = ? AND user_id = ?", workspaceID, targetUserID).First(&row).Error
		if err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			newRow := model.WorkspaceUser{WorkspaceID: workspaceID, UserID: targetUserID, Role: role}
			return tx.Create(&newRow).Error
		}
		if row.Role == "owner" {
			return errors.New("cannot change owner role")
		}
		return tx.Model(&row).Update("role", role).Error
	})
}

func (r *CollaborationRepo) ListWorkspaceMembers(ctx context.Context, workspaceID int64) ([]contract.CollaborationWorkspaceMemberRow, error) {
	rows := make([]contract.CollaborationWorkspaceMemberRow, 0)
	err := r.db.WithContext(ctx).
		Table("workspace_user wu").
		Select("wu.user_id, u.username, wu.role").
		Joins("JOIN user u ON u.id = wu.user_id").
		Where("wu.workspace_id = ?", workspaceID).
		Order("wu.id ASC").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *CollaborationRepo) UpdateWorkspaceMemberRole(ctx context.Context, workspaceID int64, targetUserID int64, role string) error {
	return r.db.WithContext(ctx).
		Model(&model.WorkspaceUser{}).
		Where("workspace_id = ? AND user_id = ?", workspaceID, targetUserID).
		Update("role", role).Error
}

func (r *CollaborationRepo) DeleteWorkspaceMember(ctx context.Context, workspaceID int64, targetUserID int64) error {
	return r.db.WithContext(ctx).
		Where("workspace_id = ? AND user_id = ?", workspaceID, targetUserID).
		Delete(&model.WorkspaceUser{}).Error
}

func (r *CollaborationRepo) GetActiveFileByID(ctx context.Context, fileID int64) (model.File, error) {
	var f model.File
	err := r.db.WithContext(ctx).First(&f, "id = ? AND is_deleted = 0", fileID).Error
	if err != nil {
		return model.File{}, err
	}
	return f, nil
}

func (r *CollaborationRepo) CountFilePermissionsByLevels(ctx context.Context, fileID int64, userID int64, permissions []string) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Model(&model.FilePermission{}).
		Where("file_id = ? AND user_id = ? AND permission IN ?", fileID, userID, permissions).
		Count(&count).Error
	if err != nil {
		return 0, err
	}
	return count, nil
}

func (r *CollaborationRepo) UpsertFilePermission(ctx context.Context, fileID int64, targetUserID int64, permission string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row model.FilePermission
		err := tx.Where("file_id = ? AND user_id = ?", fileID, targetUserID).First(&row).Error
		if err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			newRow := model.FilePermission{FileID: fileID, UserID: targetUserID, Permission: permission}
			return tx.Create(&newRow).Error
		}
		return tx.Model(&row).Update("permission", permission).Error
	})
}

func (r *CollaborationRepo) ListFilePermissions(ctx context.Context, fileID int64) ([]contract.CollaborationFilePermissionRow, error) {
	rows := make([]contract.CollaborationFilePermissionRow, 0)
	err := r.db.WithContext(ctx).
		Table("file_permission fp").
		Select("fp.user_id, u.username, fp.permission").
		Joins("JOIN user u ON u.id = fp.user_id").
		Where("fp.file_id = ?", fileID).
		Order("fp.id ASC").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *CollaborationRepo) DeleteFilePermission(ctx context.Context, fileID int64, targetUserID int64) error {
	return r.db.WithContext(ctx).Where("file_id = ? AND user_id = ?", fileID, targetUserID).Delete(&model.FilePermission{}).Error
}

func (r *CollaborationRepo) CreateFileVersion(ctx context.Context, req contract.CollaborationCreateFileVersionInput) (model.FileVersion, error) {
	owner, errOwner := r.versionOwner(ctx, req.UserID, req.FileID)
	if errOwner != nil {
		return model.FileVersion{}, errOwner
	}
	var created model.FileVersion
	err := fileMetadataTransaction(ctx, r.db, func(tx *gorm.DB) error {
		if err := quota.Begin(tx, owner); err != nil {
			return err
		}
		created = model.FileVersion{}
		var file model.File
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&file, "id=? AND is_deleted=0", req.FileID).Error; err != nil {
			return err
		}
		if file.UserID != owner {
			return contract.ErrConflict
		}
		if file.IsFolder == 1 {
			return fmt.Errorf("folder does not support versioning")
		}
		if req.UserID <= 0 {
			return contract.ErrForbidden
		}
		if err := requireFilePermission(tx, req.UserID, file, true); err != nil {
			return err
		}
		if req.BaseVersion != file.CurrentVersion {
			return contract.ErrConflict
		}
		// A storage ID is not a capability. New bytes still require an active file
		// owned by this caller; restoring this file's own history uses another path.
		var source model.File
		err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("storage_id=? AND user_id=? AND is_deleted=0 AND is_folder=0", req.StorageID, req.UserID).First(&source).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return contract.ErrForbidden
		}
		if err != nil {
			return err
		}
		var storage model.Storage
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).First(&storage, req.StorageID).Error; err != nil {
			return err
		}
		if err := verifyStorageObject(ctx, storage); err != nil {
			return err
		}
		if req.Size != nil && *req.Size != *storage.FileSize {
			return contract.ErrInvalidInput
		}
		var errCreate error
		created, errCreate = appendFileVersion(tx, file, storage, req.UserID, req.FileType, req.Comment, "file_version_create", req.ClientIP)
		return errCreate
	})
	return created, err
}

func appendFileVersion(tx *gorm.DB, file model.File, storage model.Storage, uid int64, fileType, comment, action, clientIP string) (model.FileVersion, error) {
	if file.CurrentVersion <= 0 || file.CurrentVersion >= 2147483647 {
		return model.FileVersion{}, contract.ErrConflict
	}
	if err := preserveCurrentVersion(tx, file, nil, "Legacy current version preserved before update"); err != nil {
		return model.FileVersion{}, err
	}
	if fileType == "" {
		fileType = file.FileType
	}
	size := *storage.FileSize
	base := file.CurrentVersion
	row := model.FileVersion{FileID: file.ID, Version: file.CurrentVersion + 1, StorageID: storage.ID, Size: &size, FileType: fileType, SourceVerified: true, CreateUser: &uid, Comment: comment}
	if err := tx.Create(&row).Error; err != nil {
		return row, err
	}
	if err := tx.Model(&file).Updates(map[string]any{"current_version": row.Version, "storage_id": storage.ID, "size": size, "file_type": fileType}).Error; err != nil {
		return row, err
	}
	if err := quota.Refresh(tx, file.UserID, true); err != nil {
		return row, err
	}
	err := tx.Create(&model.OperationLog{UserID: &uid, FileID: &file.ID, Action: action, Status: "success", Detail: fmt.Sprintf("version=%d base=%d", row.Version, base), ClientIP: clientIP}).Error
	return row, err
}

func (r *CollaborationRepo) RestoreFileVersion(ctx context.Context, uid, fileID int64, version, baseVersion int, clientIP string) (model.FileVersion, error) {
	owner, errOwner := r.versionOwner(ctx, uid, fileID)
	if errOwner != nil {
		return model.FileVersion{}, errOwner
	}
	var restored model.FileVersion
	err := fileMetadataTransaction(ctx, r.db, func(tx *gorm.DB) error {
		if err := quota.Begin(tx, owner); err != nil {
			return err
		}
		restored = model.FileVersion{}
		var file model.File
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&file, "id=? AND is_deleted=0", fileID).Error; err != nil {
			return err
		}
		if file.UserID != owner {
			return contract.ErrConflict
		}
		if uid <= 0 {
			return contract.ErrForbidden
		}
		if err := requireFilePermission(tx, uid, file, true); err != nil {
			return err
		}
		if file.IsFolder != 0 || version <= 0 || baseVersion <= 0 {
			return contract.ErrInvalidInput
		}
		if baseVersion != file.CurrentVersion {
			return contract.ErrConflict
		}
		var historical model.FileVersion
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("file_id=? AND version=?", fileID, version).First(&historical).Error; err != nil {
			return err
		}
		if err := requireHistoricalSource(tx, uid, file, historical, true); err != nil {
			return err
		}
		var storage model.Storage
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).First(&storage, historical.StorageID).Error; err != nil {
			return err
		}
		if err := verifyStorageObject(ctx, storage); err != nil {
			return err
		}
		if historical.Size == nil || *historical.Size != *storage.FileSize {
			return contract.ErrConflict
		}
		fileType := historical.FileType
		if fileType == "" {
			fileType = strings.TrimPrefix(strings.ToLower(filepath.Ext(file.Filename)), ".")
		}
		var err error
		restored, err = appendFileVersion(tx, file, storage, uid, fileType, fmt.Sprintf("Restored version %d", version), "file_version_restore", clientIP)
		return err
	})
	return restored, err
}

func (r *CollaborationRepo) ListFileVersions(ctx context.Context, fileID int64) ([]contract.CollaborationFileVersionRow, error) {
	rows := make([]contract.CollaborationFileVersionRow, 0)
	err := r.db.WithContext(ctx).
		Table("file_version fv").
		Select("fv.id, fv.version, fv.storage_id, fv.size, fv.file_type, fv.source_verified, fv.comment, fv.create_user, u.username, fv.create_time").
		Joins("LEFT JOIN user u ON u.id = fv.create_user").
		Where("fv.file_id = ?", fileID).
		Order("fv.version DESC").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *CollaborationRepo) CreateOperationLog(ctx context.Context, row model.OperationLog) error {
	return r.db.WithContext(ctx).Create(&row).Error
}

func (r *CollaborationRepo) versionOwner(ctx context.Context, uid, fileID int64) (int64, error) {
	var file model.File
	if err := r.db.WithContext(ctx).First(&file, "id=? AND is_deleted=0", fileID).Error; err != nil {
		return 0, err
	}
	if err := requireFilePermission(r.db.WithContext(ctx), uid, file, true); err != nil {
		return 0, err
	}
	return file.UserID, nil
}
