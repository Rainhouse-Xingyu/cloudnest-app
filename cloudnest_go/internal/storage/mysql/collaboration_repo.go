package mysql

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"cloudenest_go/internal/indexqueue"
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

func (r *CollaborationRepo) CreateWorkspace(ctx context.Context, ws *model.Workspace, ownerID int64, clientIP string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(ws).Error; err != nil {
			return err
		}
		if err := lockFileNamespaces(tx, fileNamespaceKey(ownerID, 0)); err != nil {
			return err
		}
		// An internal name keeps the workspace root independent of display name.
		name, err := allocateFileName(tx, ownerID, 0, fmt.Sprintf("Workspace %d", ws.ID), true, 0, true)
		if err != nil {
			return err
		}
		root := model.File{UserID: ownerID, Filename: name, IsFolder: 1, WorkspaceID: &ws.ID}
		if err := tx.Create(&root).Error; err != nil {
			return err
		}
		ws.RootFileID = &root.ID
		if err := tx.Model(ws).Update("root_file_id", root.ID).Error; err != nil {
			return err
		}
		row := model.WorkspaceUser{WorkspaceID: ws.ID, UserID: ownerID, Role: "owner"}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		return workspaceAudit(tx, ownerID, "workspace_create", fmt.Sprintf("workspace_id=%d", ws.ID), clientIP)
	})
}

func (r *CollaborationRepo) ListWorkspaces(ctx context.Context, uid int64) ([]contract.CollaborationWorkspaceListRow, error) {
	items := make([]contract.CollaborationWorkspaceListRow, 0)
	err := r.db.WithContext(ctx).
		Table("workspace w").
		Select("w.id, w.name, w.owner_id, w.root_file_id, wu.role, w.create_time").
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

func (r *CollaborationRepo) ChangeWorkspaceMember(ctx context.Context, change contract.WorkspaceMemberChange) error {
	if change.Role != "" && !contract.ValidWorkspaceMemberRole(change.Role) {
		return contract.ErrInvalidInput
	}
	return fileMetadataTransaction(ctx, r.db, func(tx *gorm.DB) error {
		// Serialize all membership decisions for this workspace, including
		// a change that removes the authority of another pending request.
		var workspace model.Workspace
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&workspace, change.WorkspaceID).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return contract.ErrForbidden
		}
		if err != nil {
			return err
		}
		actorRole := "owner"
		if workspace.OwnerID != change.ActorID {
			var actor model.WorkspaceUser
			err := tx.First(&actor, "workspace_id=? AND user_id=?", workspace.ID, change.ActorID).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return contract.ErrForbidden
			}
			if err != nil {
				return err
			}
			if actor.Role != "admin" || change.Role == "admin" {
				return contract.ErrForbidden
			}
			actorRole = actor.Role
		}

		var target model.WorkspaceUser
		err = tx.First(&target, "workspace_id=? AND user_id=?", workspace.ID, change.TargetID).Error
		exists := err == nil
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if change.TargetID == workspace.OwnerID || target.Role == "owner" || (actorRole == "admin" && target.Role == "admin") {
			return contract.ErrForbidden
		}
		action := "workspace_member_upsert"
		switch {
		case change.Role == "":
			if !exists {
				return gorm.ErrRecordNotFound
			}
			action = "workspace_member_remove"
			err = tx.Delete(&target).Error
		case !exists:
			if change.UpdateOnly {
				return gorm.ErrRecordNotFound
			}
			var user model.User
			if err := tx.First(&user, "id=? AND status=1", change.TargetID).Error; err != nil {
				return err
			}
			err = tx.Create(&model.WorkspaceUser{WorkspaceID: workspace.ID, UserID: change.TargetID, Role: change.Role}).Error
		default:
			if change.UpdateOnly {
				action = "workspace_member_role_update"
			}
			err = tx.Model(&target).Update("role", change.Role).Error
		}
		if err != nil {
			return err
		}
		detail := fmt.Sprintf("workspace_id=%d actor_role=%s target_user=%d role=%s", workspace.ID, actorRole, change.TargetID, change.Role)
		return workspaceAudit(tx, change.ActorID, action, detail, change.ClientIP)
	})
}

func workspaceAudit(tx *gorm.DB, uid int64, action, detail, clientIP string) error {
	return tx.Create(&model.OperationLog{UserID: &uid, Action: action, Status: "success", Detail: detail, ClientIP: clientIP}).Error
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

func (r *CollaborationRepo) GetActiveFileByID(ctx context.Context, fileID int64) (model.File, error) {
	var f model.File
	err := r.db.WithContext(ctx).First(&f, "id = ? AND is_deleted = 0", fileID).Error
	if err != nil {
		return model.File{}, err
	}
	return f, nil
}

func (r *CollaborationRepo) CanAccessFile(ctx context.Context, uid, id int64, write bool) (bool, error) {
	return canAccessFileForFileRepo(ctx, r.db, uid, id, write)
}

func (r *CollaborationRepo) ChangeFilePermission(ctx context.Context, actorID, fileID, targetUserID int64, permission, clientIP string) error {
	if permission != "" && permission != "read" && permission != "write" {
		return contract.ErrInvalidInput
	}
	return fileMetadataTransaction(ctx, r.db, func(tx *gorm.DB) error {
		var file model.File
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&file, "id=? AND is_deleted=0", fileID).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return contract.ErrForbidden
		}
		if err != nil {
			return err
		}
		if file.UserID != actorID {
			return contract.ErrForbidden
		}
		ws, err := fileWorkspace(tx, file)
		if err != nil {
			return err
		}
		if ws.ID != 0 {
			return contract.ErrForbidden
		}
		action := "file_permission_revoke"
		if permission == "" {
			result := tx.Where("file_id=? AND user_id=?", fileID, targetUserID).Delete(&model.FilePermission{})
			if result.Error != nil || result.RowsAffected == 0 {
				return result.Error
			}
		} else {
			action = "file_permission_upsert"
			var user model.User
			if err := tx.First(&user, "id=? AND status=1", targetUserID).Error; err != nil {
				return err
			}
			var existing int64
			query := tx.Model(&model.FilePermission{}).Where("file_id=? AND user_id=?", fileID, targetUserID)
			if err := query.Count(&existing).Error; err != nil {
				return err
			}
			if existing == 0 {
				err = tx.Create(&model.FilePermission{FileID: fileID, UserID: targetUserID, Permission: permission}).Error
			} else {
				// Historical duplicate grants are retained but all are downgraded together.
				err = tx.Model(&model.FilePermission{}).Where("file_id=? AND user_id=?", fileID, targetUserID).Update("permission", permission).Error
			}
			if err != nil {
				return err
			}
		}
		detail := fmt.Sprintf("file_id=%d target_user=%d permission=%s", fileID, targetUserID, permission)
		return tx.Create(&model.OperationLog{UserID: &actorID, FileID: &fileID, Action: action, Status: "success", Detail: detail, ClientIP: clientIP}).Error
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
		candidates := tx.Table("file f").Select("f.*").
			Where("f.storage_id=? AND f.user_id=? AND f.is_deleted=0 AND f.is_folder=0", req.StorageID, req.UserID).
			Where("(f.workspace_id <=> ? OR NOT EXISTS (SELECT 1 FROM workspace w WHERE w.id=f.workspace_id AND w.root_file_id IS NOT NULL))", file.WorkspaceID)
		err := fileAccessScope(candidates, "f", req.UserID, false).Clauses(clause.Locking{Strength: "SHARE"}).Order("f.id").Take(&source).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return contract.ErrForbidden
		}
		if err != nil {
			return err
		}
		if err := requireFilePermission(tx, req.UserID, source, false); err != nil {
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
	if err := indexqueue.Enqueue(tx, file.ID, true); err != nil {
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
