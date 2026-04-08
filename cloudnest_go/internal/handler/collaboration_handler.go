package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"cloudenest_go/internal/middleware"
	"cloudenest_go/internal/service"
	"cloudenest_go/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CollaborationHandler 处理多人协作相关接口。
type CollaborationHandler struct {
	DB  *gorm.DB
	Svc *service.CollaborationService
}

// CreateWorkspaceRequest 创建团队空间请求。
type CreateWorkspaceRequest struct {
	Name string `json:"name" binding:"required"`
}

// AddWorkspaceMemberRequest 邀请空间成员请求。
type AddWorkspaceMemberRequest struct {
	UserID   *int64 `json:"user_id"`
	Username string `json:"username"`
	Role     string `json:"role" binding:"required"`
}

// UpdateWorkspaceMemberRoleRequest 更新空间成员角色请求。
type UpdateWorkspaceMemberRoleRequest struct {
	Role string `json:"role" binding:"required"`
}

// GrantFilePermissionRequest 授予文件协作权限请求。
type GrantFilePermissionRequest struct {
	UserID     *int64 `json:"user_id"`
	Username   string `json:"username"`
	Permission string `json:"permission" binding:"required"`
}

// CreateFileVersionRequest 创建文件版本请求。
type CreateFileVersionRequest struct {
	StorageID   int64  `json:"storage_id" binding:"required"`
	Comment     string `json:"comment"`
	BaseVersion int    `json:"base_version" binding:"required"`
	Size        *int64 `json:"size"`
	FileType    string `json:"file_type"`
}

// CreateWorkspace 创建团队空间并自动把创建者加入成员列表。
func (h *CollaborationHandler) CreateWorkspace(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)

	var req CreateWorkspaceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "workspace name is required"})
		return
	}

	ws := model.Workspace{Name: req.Name, OwnerID: uid}
	err := h.Svc.CreateWorkspace(c.Request.Context(), &ws, uid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "create workspace failed"})
		return
	}

	_ = h.Svc.LogOperation(c.Request.Context(), uid, nil, "workspace_create", "success", fmt.Sprintf("workspace_id=%d", ws.ID), c.ClientIP())
	c.JSON(http.StatusOK, gin.H{"item": ws})
}

// ListWorkspaces 查询当前用户可访问的团队空间。
func (h *CollaborationHandler) ListWorkspaces(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)

	items, err := h.Svc.ListWorkspaces(c.Request.Context(), uid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"items": items})
}

// AddWorkspaceMember 邀请成员加入空间。
func (h *CollaborationHandler) AddWorkspaceMember(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)
	workspaceID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || workspaceID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid workspace id"})
		return
	}

	var req AddWorkspaceMemberRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	req.Role = normalizeWorkspaceRole(req.Role)
	if req.Role == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "role should be owner/admin/member"})
		return
	}

	if req.Role == "owner" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cannot assign owner role"})
		return
	}

	role, inWorkspace, err := h.Svc.GetWorkspaceRole(c.Request.Context(), workspaceID, uid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if !inWorkspace || !canManageWorkspace(role) {
		c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
		return
	}

	targetUser, err := h.Svc.ResolveTargetUser(c.Request.Context(), req.UserID, req.Username)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "target user not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}

	err = h.Svc.UpsertWorkspaceMember(c.Request.Context(), workspaceID, targetUser.ID, req.Role)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "cannot change owner") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "cannot change owner role"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "save workspace member failed"})
		return
	}

	detail := fmt.Sprintf("workspace_id=%d actor_role=%s target_user=%d role=%s", workspaceID, role, targetUser.ID, req.Role)
	_ = h.Svc.LogOperation(c.Request.Context(), uid, nil, "workspace_member_upsert", "success", detail, c.ClientIP())
	c.JSON(http.StatusOK, gin.H{"message": "member updated"})
}

// ListWorkspaceMembers 查询空间成员列表。
func (h *CollaborationHandler) ListWorkspaceMembers(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)
	workspaceID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || workspaceID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid workspace id"})
		return
	}

	_, canView, err := h.Svc.GetWorkspaceRole(c.Request.Context(), workspaceID, uid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if !canView {
		c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
		return
	}

	rows, err := h.Svc.ListWorkspaceMembers(c.Request.Context(), workspaceID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"items": rows})
}

// UpdateWorkspaceMemberRole 更新空间成员角色。
func (h *CollaborationHandler) UpdateWorkspaceMemberRole(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)
	workspaceID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || workspaceID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid workspace id"})
		return
	}
	targetUserID, err := strconv.ParseInt(c.Param("user_id"), 10, 64)
	if err != nil || targetUserID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid target user id"})
		return
	}

	var req UpdateWorkspaceMemberRoleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	req.Role = normalizeWorkspaceRole(req.Role)
	if req.Role == "" || req.Role == "owner" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "role should be admin/member"})
		return
	}

	role, inWorkspace, err := h.Svc.GetWorkspaceRole(c.Request.Context(), workspaceID, uid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if !inWorkspace || !canManageWorkspace(role) {
		c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
		return
	}

	target, err := h.Svc.GetWorkspaceMember(c.Request.Context(), workspaceID, targetUserID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "workspace member not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if target.Role == "owner" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cannot change owner role"})
		return
	}

	if err := h.Svc.UpdateWorkspaceMemberRole(c.Request.Context(), &target, req.Role); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "update member role failed"})
		return
	}

	detail := fmt.Sprintf("workspace_id=%d actor_role=%s target_user=%d role=%s", workspaceID, role, targetUserID, req.Role)
	_ = h.Svc.LogOperation(c.Request.Context(), uid, nil, "workspace_member_role_update", "success", detail, c.ClientIP())
	c.JSON(http.StatusOK, gin.H{"message": "member role updated"})
}

// RemoveWorkspaceMember 移除空间成员。
func (h *CollaborationHandler) RemoveWorkspaceMember(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)
	workspaceID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || workspaceID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid workspace id"})
		return
	}
	targetUserID, err := strconv.ParseInt(c.Param("user_id"), 10, 64)
	if err != nil || targetUserID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid target user id"})
		return
	}

	role, inWorkspace, err := h.Svc.GetWorkspaceRole(c.Request.Context(), workspaceID, uid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if !inWorkspace || !canManageWorkspace(role) {
		c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
		return
	}

	target, err := h.Svc.GetWorkspaceMember(c.Request.Context(), workspaceID, targetUserID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "workspace member not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if target.Role == "owner" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cannot remove owner"})
		return
	}

	if err := h.Svc.DeleteWorkspaceMember(c.Request.Context(), &target); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "remove member failed"})
		return
	}

	detail := fmt.Sprintf("workspace_id=%d actor_role=%s target_user=%d", workspaceID, role, targetUserID)
	_ = h.Svc.LogOperation(c.Request.Context(), uid, nil, "workspace_member_remove", "success", detail, c.ClientIP())
	c.JSON(http.StatusOK, gin.H{"message": "member removed"})
}

// GrantFilePermission 邀请指定用户协作文件，支持 read/write 权限。
func (h *CollaborationHandler) GrantFilePermission(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)
	fileID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || fileID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid file id"})
		return
	}

	var req GrantFilePermissionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	req.Permission = normalizeFilePermission(req.Permission)
	if req.Permission == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "permission should be read/write"})
		return
	}

	canWrite, err := h.Svc.CanAccessFile(c.Request.Context(), uid, fileID, true)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if !canWrite {
		c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
		return
	}

	targetUser, err := h.Svc.ResolveTargetUser(c.Request.Context(), req.UserID, req.Username)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "target user not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if targetUser.ID == uid {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cannot grant permission to self"})
		return
	}

	err = h.Svc.UpsertFilePermission(c.Request.Context(), fileID, targetUser.ID, req.Permission)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "save file permission failed"})
		return
	}

	detail := fmt.Sprintf("file_id=%d target_user=%d permission=%s", fileID, targetUser.ID, req.Permission)
	_ = h.Svc.LogOperation(c.Request.Context(), uid, &fileID, "file_permission_upsert", "success", detail, c.ClientIP())
	c.JSON(http.StatusOK, gin.H{"message": "file permission updated"})
}

// ListFilePermissions 查询文件协作用户及权限。
func (h *CollaborationHandler) ListFilePermissions(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)
	fileID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || fileID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid file id"})
		return
	}

	canRead, err := h.Svc.CanAccessFile(c.Request.Context(), uid, fileID, false)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if !canRead {
		c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
		return
	}

	rows, err := h.Svc.ListFilePermissions(c.Request.Context(), fileID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"items": rows})
}

// RevokeFilePermission 取消指定协作者的文件权限。
func (h *CollaborationHandler) RevokeFilePermission(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)
	fileID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || fileID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid file id"})
		return
	}
	targetUserID, err := strconv.ParseInt(c.Param("user_id"), 10, 64)
	if err != nil || targetUserID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid target user id"})
		return
	}

	canWrite, err := h.Svc.CanAccessFile(c.Request.Context(), uid, fileID, true)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if !canWrite {
		c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
		return
	}

	if err := h.Svc.RevokeFilePermission(c.Request.Context(), fileID, targetUserID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "revoke file permission failed"})
		return
	}

	detail := fmt.Sprintf("file_id=%d target_user=%d", fileID, targetUserID)
	_ = h.Svc.LogOperation(c.Request.Context(), uid, &fileID, "file_permission_revoke", "success", detail, c.ClientIP())
	c.JSON(http.StatusOK, gin.H{"message": "file permission revoked"})
}

// CreateFileVersion 保存新版本，采用“基于上一版本提交”的协作策略。
func (h *CollaborationHandler) CreateFileVersion(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)
	fileID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || fileID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid file id"})
		return
	}

	var req CreateFileVersionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	req.Comment = strings.TrimSpace(req.Comment)
	req.FileType = strings.ToLower(strings.TrimSpace(req.FileType))
	if req.StorageID <= 0 || req.BaseVersion <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "storage_id and base_version are required"})
		return
	}

	canWrite, err := h.canAccessFile(c.Request.Context(), uid, fileID, true)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if !canWrite {
		c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
		return
	}

	var created model.FileVersion
	err = h.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var f model.File
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&f, "id = ? AND is_deleted = 0", fileID).Error; err != nil {
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

		uidCopy := uid
		sizeCopy := sizeVal
		created = model.FileVersion{
			FileID:     fileID,
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
		return tx.Model(&model.File{}).Where("id = ?", fileID).Updates(updates).Error
	})
	if err != nil {
		msg := strings.ToLower(err.Error())
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": "file or storage not found"})
			return
		case strings.Contains(msg, "folder does not support"):
			c.JSON(http.StatusBadRequest, gin.H{"error": "folder does not support versioning"})
			return
		case strings.Contains(msg, "version conflict"):
			c.JSON(http.StatusConflict, gin.H{"error": "base_version is outdated"})
			return
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": "create file version failed"})
			return
		}
	}

	detail := fmt.Sprintf("file_id=%d version=%d base=%d", fileID, created.Version, req.BaseVersion)
	_ = h.Svc.LogOperation(c.Request.Context(), uid, &fileID, "file_version_create", "success", detail, c.ClientIP())
	c.JSON(http.StatusOK, gin.H{"item": created})
}

// ListFileVersions 查询文件版本历史（倒序）。
func (h *CollaborationHandler) ListFileVersions(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)
	fileID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || fileID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid file id"})
		return
	}

	canRead, err := h.canAccessFile(c.Request.Context(), uid, fileID, false)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if !canRead {
		c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
		return
	}

	type row struct {
		ID         int64     `json:"id"`
		Version    int       `json:"version"`
		StorageID  int64     `json:"storage_id"`
		Size       *int64    `json:"size"`
		Comment    string    `json:"comment"`
		CreateUser *int64    `json:"create_user"`
		Username   string    `json:"username"`
		CreateTime time.Time `json:"create_time"`
	}

	rows := make([]row, 0)
	err = h.DB.WithContext(c.Request.Context()).
		Table("file_version fv").
		Select("fv.id, fv.version, fv.storage_id, fv.size, fv.comment, fv.create_user, u.username, fv.create_time").
		Joins("LEFT JOIN user u ON u.id = fv.create_user").
		Where("fv.file_id = ?", fileID).
		Order("fv.version DESC").
		Find(&rows).Error
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"items": rows})
}

// canAccessFile 判断是否有文件访问权限（写权限要求更严格）。
func (h *CollaborationHandler) canAccessFile(ctx context.Context, userID int64, fileID int64, requireWrite bool) (bool, error) {
	var f model.File
	err := h.DB.WithContext(ctx).First(&f, "id = ? AND is_deleted = 0", fileID).Error
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
	err = h.DB.WithContext(ctx).
		Model(&model.FilePermission{}).
		Where("file_id = ? AND user_id = ? AND permission IN ?", fileID, userID, permissions).
		Count(&count).Error
	if err != nil {
		return false, err
	}

	return count > 0, nil
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

func normalizeFilePermission(p string) string {
	v := strings.ToLower(strings.TrimSpace(p))
	switch v {
	case "read", "write":
		return v
	default:
		return ""
	}
}

func canManageWorkspace(role string) bool {
	return role == "owner" || role == "admin"
}
