package service

import (
	"context"
	"errors"
	"strings"

	"cloudenest_go/internal/service/contract"
	"cloudenest_go/model"

	"gorm.io/gorm"
)

// CollaborationService 提供协作模块的数据与事务能力。
type CollaborationService struct {
	repo contract.CollaborationRepository
}

func NewCollaborationService(repo contract.CollaborationRepository) *CollaborationService {
	return &CollaborationService{repo: repo}
}

// WorkspaceListRow 团队空间列表项。
type WorkspaceListRow = contract.CollaborationWorkspaceListRow

// WorkspaceMemberRow 团队成员列表项。
type WorkspaceMemberRow = contract.CollaborationWorkspaceMemberRow

// FilePermissionRow 文件协作权限列表项。
type FilePermissionRow = contract.CollaborationFilePermissionRow

// CreateFileVersionInput 创建文件版本输入参数。
type CreateFileVersionInput = contract.CollaborationCreateFileVersionInput

// FileVersionRow 文件版本查询返回行。
type FileVersionRow = contract.CollaborationFileVersionRow

// CreateWorkspace 创建空间并写入 owner 成员关系。
func (s *CollaborationService) CreateWorkspace(ctx context.Context, ws *model.Workspace, ownerID int64) error {
	return s.repo.CreateWorkspace(ctx, ws, ownerID)
}

// ListWorkspaces 查询用户可访问空间。
func (s *CollaborationService) ListWorkspaces(ctx context.Context, uid int64) ([]WorkspaceListRow, error) {
	return s.repo.ListWorkspaces(ctx, uid)
}

// GetWorkspaceRole 查询用户在空间中的角色与是否存在成员关系。
func (s *CollaborationService) GetWorkspaceRole(ctx context.Context, workspaceID int64, userID int64) (string, bool, error) {
	row, err := s.repo.GetWorkspaceMember(ctx, workspaceID, userID)
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
	if userID != nil && *userID > 0 {
		return s.repo.GetUserByID(ctx, *userID)
	}
	username = strings.TrimSpace(username)
	if username == "" {
		return model.User{}, gorm.ErrRecordNotFound
	}
	return s.repo.GetUserByUsername(ctx, username)
}

// UpsertWorkspaceMember 新增或更新空间成员。
func (s *CollaborationService) UpsertWorkspaceMember(ctx context.Context, workspaceID int64, targetUserID int64, role string) error {
	return s.repo.UpsertWorkspaceMember(ctx, workspaceID, targetUserID, role)
}

// ListWorkspaceMembers 查询空间成员。
func (s *CollaborationService) ListWorkspaceMembers(ctx context.Context, workspaceID int64) ([]WorkspaceMemberRow, error) {
	return s.repo.ListWorkspaceMembers(ctx, workspaceID)
}

// GetWorkspaceMember 查询空间成员记录。
func (s *CollaborationService) GetWorkspaceMember(ctx context.Context, workspaceID int64, targetUserID int64) (model.WorkspaceUser, error) {
	return s.repo.GetWorkspaceMember(ctx, workspaceID, targetUserID)
}

// UpdateWorkspaceMemberRole 更新空间成员角色。
func (s *CollaborationService) UpdateWorkspaceMemberRole(ctx context.Context, target *model.WorkspaceUser, role string) error {
	return s.repo.UpdateWorkspaceMemberRole(ctx, target.WorkspaceID, target.UserID, role)
}

// DeleteWorkspaceMember 删除空间成员。
func (s *CollaborationService) DeleteWorkspaceMember(ctx context.Context, target *model.WorkspaceUser) error {
	return s.repo.DeleteWorkspaceMember(ctx, target.WorkspaceID, target.UserID)
}

// CanAccessFile 判断用户是否有文件访问权限（写权限要求更严格）。
func (s *CollaborationService) CanAccessFile(ctx context.Context, userID int64, fileID int64, requireWrite bool) (bool, error) {
	f, err := s.repo.GetActiveFileByID(ctx, fileID)
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

	count, err := s.repo.CountFilePermissionsByLevels(ctx, fileID, userID, permissions)
	if err != nil {
		return false, err
	}

	return count > 0, nil
}

// UpsertFilePermission 新增或更新文件协作权限。
func (s *CollaborationService) UpsertFilePermission(ctx context.Context, fileID int64, targetUserID int64, permission string) error {
	return s.repo.UpsertFilePermission(ctx, fileID, targetUserID, permission)
}

// ListFilePermissions 查询文件协作权限列表。
func (s *CollaborationService) ListFilePermissions(ctx context.Context, fileID int64) ([]FilePermissionRow, error) {
	return s.repo.ListFilePermissions(ctx, fileID)
}

// RevokeFilePermission 删除指定文件协作权限。
func (s *CollaborationService) RevokeFilePermission(ctx context.Context, fileID int64, targetUserID int64) error {
	return s.repo.DeleteFilePermission(ctx, fileID, targetUserID)
}

// CreateFileVersion 保存新版本，采用“基于上一版本提交”的协作策略。
func (s *CollaborationService) CreateFileVersion(ctx context.Context, req CreateFileVersionInput) (model.FileVersion, error) {
	return s.repo.CreateFileVersion(ctx, req)
}

// ListFileVersions 查询文件版本历史（倒序）。
func (s *CollaborationService) ListFileVersions(ctx context.Context, fileID int64) ([]FileVersionRow, error) {
	return s.repo.ListFileVersions(ctx, fileID)
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
	return s.repo.CreateOperationLog(ctx, row)
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
