package mysql

import (
	"context"
	"errors"
	"fmt"

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
	var created model.FileVersion
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var f model.File
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&f, "id = ? AND is_deleted = 0", req.FileID).Error; err != nil {
			return err
		}
		if f.IsFolder == 1 {
			return fmt.Errorf("folder does not support versioning")
		}
		if req.BaseVersion != f.CurrentVersion {
			return fmt.Errorf("version conflict")
		}

		var storage model.Storage
		if err := tx.First(&storage, "id = ?", req.StorageID).Error; err != nil {
			return err
		}

		nextVersion := f.CurrentVersion + 1
		sizeVal := f.Size
		if req.Size != nil {
			sizeVal = *req.Size
		} else if storage.FileSize != nil {
			sizeVal = *storage.FileSize
		}

		uidCopy := req.UserID
		sizeCopy := sizeVal
		created = model.FileVersion{
			FileID:     req.FileID,
			Version:    nextVersion,
			StorageID:  req.StorageID,
			Size:       &sizeCopy,
			CreateUser: &uidCopy,
			Comment:    req.Comment,
		}
		if err := tx.Create(&created).Error; err != nil {
			return err
		}

		updates := map[string]interface{}{
			"current_version": nextVersion,
			"storage_id":      req.StorageID,
			"size":            sizeVal,
		}
		if req.FileType != "" {
			updates["file_type"] = req.FileType
		}
		return tx.Model(&model.File{}).Where("id = ?", req.FileID).Updates(updates).Error
	})
	if err != nil {
		return model.FileVersion{}, err
	}
	return created, nil
}

func (r *CollaborationRepo) ListFileVersions(ctx context.Context, fileID int64) ([]contract.CollaborationFileVersionRow, error) {
	rows := make([]contract.CollaborationFileVersionRow, 0)
	err := r.db.WithContext(ctx).
		Table("file_version fv").
		Select("fv.id, fv.version, fv.storage_id, fv.size, fv.comment, fv.create_user, u.username, fv.create_time").
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
