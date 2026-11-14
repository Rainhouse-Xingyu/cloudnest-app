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
	RootFileID *int64    `json:"root_file_id"`
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
	ClientIP    string
	BaseVersion int
	Comment     string
	FileType    string
	Size        *int64
	UserID      int64
}

// CollaborationFileVersionRow 定义文件版本查询返回行。
type CollaborationFileVersionRow struct {
	ID             int64     `json:"id"`
	SourceVerified bool      `json:"source_verified"`
	FileType       string    `json:"file_type"`
	Version        int       `json:"version"`
	StorageID      int64     `json:"storage_id"`
	Size           *int64    `json:"size"`
	Comment        string    `json:"comment"`
	CreateUser     *int64    `json:"create_user"`
	Username       string    `json:"username"`
	CreateTime     time.Time `json:"create_time"`
}

type WorkspaceMemberChange struct {
	WorkspaceID, ActorID, TargetID int64
	Role, ClientIP                 string
	UpdateOnly                     bool
}

func ValidWorkspaceMemberRole(role string) bool {
	switch role {
	case "admin", "member", "viewer", "editor":
		return true
	default:
		return false
	}
}

// CollaborationRepository 定义协作模块存储访问能力。
type CollaborationRepository interface {
	CreateWorkspace(ctx context.Context, ws *model.Workspace, ownerID int64, clientIP string) error
	ListWorkspaces(ctx context.Context, uid int64) ([]CollaborationWorkspaceListRow, error)
	GetWorkspaceMember(ctx context.Context, workspaceID int64, userID int64) (model.WorkspaceUser, error)
	GetUserByID(ctx context.Context, userID int64) (model.User, error)
	GetUserByUsername(ctx context.Context, username string) (model.User, error)
	ChangeWorkspaceMember(ctx context.Context, change WorkspaceMemberChange) error
	ListWorkspaceMembers(ctx context.Context, workspaceID int64) ([]CollaborationWorkspaceMemberRow, error)
	GetActiveFileByID(ctx context.Context, fileID int64) (model.File, error)
	CanAccessFile(ctx context.Context, userID, fileID int64, write bool) (bool, error)
	ChangeFilePermission(ctx context.Context, actorID, fileID, targetUserID int64, permission, clientIP string) error
	ListFilePermissions(ctx context.Context, fileID int64) ([]CollaborationFilePermissionRow, error)
	CreateFileVersion(ctx context.Context, req CollaborationCreateFileVersionInput) (model.FileVersion, error)
	RestoreFileVersion(ctx context.Context, uid, fileID int64, version, baseVersion int, clientIP string) (model.FileVersion, error)
	ListFileVersions(ctx context.Context, fileID int64) ([]CollaborationFileVersionRow, error)
}
