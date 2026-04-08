package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"cloudenest_go/model"

	"gorm.io/gorm"
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
