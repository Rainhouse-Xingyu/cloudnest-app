package contract

import (
	"context"
	"time"

	"cloudenest_go/model"
)

// CollaborationWorkspaceListRow 定义团队空间列表项。
type CollaborationWorkspaceListRow struct {
	ID         int64     `json:"id"`
	Name       string    `json:"name"`
	OwnerID    int64     `json:"owner_id"`
	Role       string    `json:"role"`
	CreateTime time.Time `json:"create_time"`
}

// CollaborationWorkspaceMemberRow 定义团队成员列表项。
type CollaborationWorkspaceMemberRow struct {
	UserID   int64  `json:"user_id"`
	Username string `json:"username"`
	Role     string `json:"role"`
}

// CollaborationFilePermissionRow 定义文件协作权限列表项。
type CollaborationFilePermissionRow struct {
	UserID     int64  `json:"user_id"`
	Username   string `json:"username"`
	Permission string `json:"permission"`
}

// CollaborationCreateFileVersionInput 定义创建文件版本输入。
type CollaborationCreateFileVersionInput struct {
	FileID      int64
	StorageID   int64
	BaseVersion int
	Comment     string
	FileType    string
	Size        *int64
	UserID      int64
}

// CollaborationFileVersionRow 定义文件版本查询返回行。
type CollaborationFileVersionRow struct {
	ID         int64     `json:"id"`
	Version    int       `json:"version"`
	StorageID  int64     `json:"storage_id"`
	Size       *int64    `json:"size"`
	Comment    string    `json:"comment"`
	CreateUser *int64    `json:"create_user"`
	Username   string    `json:"username"`
	CreateTime time.Time `json:"create_time"`
}

// CollaborationRepository 定义协作模块存储访问能力。
type CollaborationRepository interface {
	CreateWorkspace(ctx context.Context, ws *model.Workspace, ownerID int64) error
	ListWorkspaces(ctx context.Context, uid int64) ([]CollaborationWorkspaceListRow, error)
	GetWorkspaceMember(ctx context.Context, workspaceID int64, userID int64) (model.WorkspaceUser, error)
	GetUserByID(ctx context.Context, userID int64) (model.User, error)
	GetUserByUsername(ctx context.Context, username string) (model.User, error)
	UpsertWorkspaceMember(ctx context.Context, workspaceID int64, targetUserID int64, role string) error
	ListWorkspaceMembers(ctx context.Context, workspaceID int64) ([]CollaborationWorkspaceMemberRow, error)
	UpdateWorkspaceMemberRole(ctx context.Context, workspaceID int64, targetUserID int64, role string) error
	DeleteWorkspaceMember(ctx context.Context, workspaceID int64, targetUserID int64) error
	GetActiveFileByID(ctx context.Context, fileID int64) (model.File, error)
	CountFilePermissionsByLevels(ctx context.Context, fileID int64, userID int64, permissions []string) (int64, error)
	UpsertFilePermission(ctx context.Context, fileID int64, targetUserID int64, permission string) error
	ListFilePermissions(ctx context.Context, fileID int64) ([]CollaborationFilePermissionRow, error)
	DeleteFilePermission(ctx context.Context, fileID int64, targetUserID int64) error
	CreateFileVersion(ctx context.Context, req CollaborationCreateFileVersionInput) (model.FileVersion, error)
	ListFileVersions(ctx context.Context, fileID int64) ([]CollaborationFileVersionRow, error)
	CreateOperationLog(ctx context.Context, row model.OperationLog) error
}
