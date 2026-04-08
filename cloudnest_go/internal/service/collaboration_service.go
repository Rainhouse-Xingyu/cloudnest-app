package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"cloudenest_go/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CollaborationService 提供协作模块的数据与事务能力。
type CollaborationService struct {
	db *gorm.DB
}

func NewCollaborationService(db *gorm.DB) *CollaborationService {
	return &CollaborationService{db: db}
}

// WorkspaceListRow 团队空间列表项。
type WorkspaceListRow struct {
	ID         int64     `json:"id"`
	Name       string    `json:"name"`
	OwnerID    int64     `json:"owner_id"`
	Role       string    `json:"role"`
	CreateTime time.Time `json:"create_time"`
}

// WorkspaceMemberRow 团队成员列表项。
type WorkspaceMemberRow struct {
	UserID   int64  `json:"user_id"`
	Username string `json:"username"`
	Role     string `json:"role"`
}

// FilePermissionRow 文件协作权限列表项。
type FilePermissionRow struct {
	UserID     int64  `json:"user_id"`
	Username   string `json:"username"`
	Permission string `json:"permission"`
}

// CreateFileVersionInput 创建文件版本输入参数。
type CreateFileVersionInput struct {
	FileID      int64
	StorageID   int64
	BaseVersion int
	Comment     string
	FileType    string
	Size        *int64
	UserID      int64
}

// FileVersionRow 文件版本查询返回行。
type FileVersionRow struct {
	ID         int64     `json:"id"`
	Version    int       `json:"version"`
	StorageID  int64     `json:"storage_id"`
	Size       *int64    `json:"size"`
	Comment    string    `json:"comment"`
	CreateUser *int64    `json:"create_user"`
	Username   string    `json:"username"`
	CreateTime time.Time `json:"create_time"`
}

// CreateWorkspace 创建空间并写入 owner 成员关系。
func (s *CollaborationService) CreateWorkspace(ctx context.Context, ws *model.Workspace, ownerID int64) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(ws).Error; err != nil {
			return err
		}
		ownerRole := "owner"
		row := model.WorkspaceUser{WorkspaceID: ws.ID, UserID: ownerID, Role: ownerRole}
		return tx.Create(&row).Error
	})
}

// ListWorkspaces 查询用户可访问空间。
func (s *CollaborationService) ListWorkspaces(ctx context.Context, uid int64) ([]WorkspaceListRow, error) {
	items := make([]WorkspaceListRow, 0)
	err := s.db.WithContext(ctx).
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

// GetWorkspaceRole 查询用户在空间中的角色与是否存在成员关系。
func (s *CollaborationService) GetWorkspaceRole(ctx context.Context, workspaceID int64, userID int64) (string, bool, error) {
	var row model.WorkspaceUser
	err := s.db.WithContext(ctx).Where("workspace_id = ? AND user_id = ?", workspaceID, userID).First(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", false, nil
		}
		return "", false, err
	}
	role := normalizeWorkspaceRole(row.Role)
	return role, true, nil
}

// ResolveTargetUser 根据 user_id 或 username 定位目标用户。
func (s *CollaborationService) ResolveTargetUser(ctx context.Context, userID *int64, username string) (model.User, error) {
	var user model.User
	if userID != nil && *userID > 0 {
		err := s.db.WithContext(ctx).First(&user, "id = ?", *userID).Error
		return user, err
	}
	username = strings.TrimSpace(username)
	if username == "" {
		return user, gorm.ErrRecordNotFound
	}
	err := s.db.WithContext(ctx).First(&user, "username = ?", username).Error
	return user, err
}

// UpsertWorkspaceMember 新增或更新空间成员。
func (s *CollaborationService) UpsertWorkspaceMember(ctx context.Context, workspaceID int64, targetUserID int64, role string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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

// ListWorkspaceMembers 查询空间成员。
func (s *CollaborationService) ListWorkspaceMembers(ctx context.Context, workspaceID int64) ([]WorkspaceMemberRow, error) {
	rows := make([]WorkspaceMemberRow, 0)
	err := s.db.WithContext(ctx).
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

// GetWorkspaceMember 查询空间成员记录。
func (s *CollaborationService) GetWorkspaceMember(ctx context.Context, workspaceID int64, targetUserID int64) (model.WorkspaceUser, error) {
	var target model.WorkspaceUser
	err := s.db.WithContext(ctx).Where("workspace_id = ? AND user_id = ?", workspaceID, targetUserID).First(&target).Error
	return target, err
}

// UpdateWorkspaceMemberRole 更新空间成员角色。
func (s *CollaborationService) UpdateWorkspaceMemberRole(ctx context.Context, target *model.WorkspaceUser, role string) error {
	return s.db.WithContext(ctx).Model(target).Update("role", role).Error
}

// DeleteWorkspaceMember 删除空间成员。
func (s *CollaborationService) DeleteWorkspaceMember(ctx context.Context, target *model.WorkspaceUser) error {
	return s.db.WithContext(ctx).Delete(target).Error
}

// CanAccessFile 判断用户是否有文件访问权限（写权限要求更严格）。
func (s *CollaborationService) CanAccessFile(ctx context.Context, userID int64, fileID int64, requireWrite bool) (bool, error) {
	var f model.File
	err := s.db.WithContext(ctx).First(&f, "id = ? AND is_deleted = 0", fileID).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
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
	err = s.db.WithContext(ctx).
		Model(&model.FilePermission{}).
		Where("file_id = ? AND user_id = ? AND permission IN ?", fileID, userID, permissions).
		Count(&count).Error
	if err != nil {
		return false, err
	}

	return count > 0, nil
}

// UpsertFilePermission 新增或更新文件协作权限。
func (s *CollaborationService) UpsertFilePermission(ctx context.Context, fileID int64, targetUserID int64, permission string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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

// ListFilePermissions 查询文件协作权限列表。
func (s *CollaborationService) ListFilePermissions(ctx context.Context, fileID int64) ([]FilePermissionRow, error) {
	rows := make([]FilePermissionRow, 0)
	err := s.db.WithContext(ctx).
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

// RevokeFilePermission 删除指定文件协作权限。
func (s *CollaborationService) RevokeFilePermission(ctx context.Context, fileID int64, targetUserID int64) error {
	return s.db.WithContext(ctx).Where("file_id = ? AND user_id = ?", fileID, targetUserID).Delete(&model.FilePermission{}).Error
}

// CreateFileVersion 保存新版本，采用“基于上一版本提交”的协作策略。
func (s *CollaborationService) CreateFileVersion(ctx context.Context, req CreateFileVersionInput) (model.FileVersion, error) {
	var created model.FileVersion
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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

// ListFileVersions 查询文件版本历史（倒序）。
func (s *CollaborationService) ListFileVersions(ctx context.Context, fileID int64) ([]FileVersionRow, error) {
	rows := make([]FileVersionRow, 0)
	err := s.db.WithContext(ctx).
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

// LogOperation 记录协作操作日志。
func (s *CollaborationService) LogOperation(ctx context.Context, userID int64, fileID *int64, action string, status string, detail string, clientIP string) error {
	uid := userID
	row := model.OperationLog{
		UserID:   &uid,
		FileID:   fileID,
		Action:   strings.TrimSpace(action),
		Status:   strings.TrimSpace(status),
		Detail:   strings.TrimSpace(detail),
		ClientIP: strings.TrimSpace(clientIP),
	}
	if row.Action == "" {
		row.Action = "unknown"
	}
	if row.Status == "" {
		row.Status = "success"
	}
	return s.db.WithContext(ctx).Create(&row).Error
}

func normalizeWorkspaceRole(role string) string {
	r := strings.ToLower(strings.TrimSpace(role))
	switch r {
	case "owner", "admin", "member":
		return r
	default:
		return ""
	}
}
