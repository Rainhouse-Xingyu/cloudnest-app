package handler

import (
	"net/http"
	"strconv"
	"strings"

	"cloudenest_go/internal/middleware"
	"cloudenest_go/internal/service"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// SettingsHandler 负责系统设置与权限聚合展示接口。
type SettingsHandler struct {
	Svc *service.SettingsService
}

type settingUpsertItem struct {
	SettingKey   string `json:"setting_key" binding:"required"`
	SettingValue string `json:"setting_value" binding:"required"`
	Description  string `json:"description"`
}

type upsertSettingsRequest struct {
	Items []settingUpsertItem `json:"items" binding:"required"`
}

type updateRateWindowRequest struct {
	Enabled *bool  `json:"enabled"`
	Start   string `json:"start"`
	End     string `json:"end"`
}

// GetSettings 返回 system_setting 全量配置。
func (h *SettingsHandler) GetSettings(c *gin.Context) {
	isAdmin, _ := c.Get(middleware.ContextIsAdminKey)
	isAdminBool, _ := isAdmin.(bool)

	rows, err := h.Svc.GetSettings(c.Request.Context(), isAdminBool)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"items": rows})
}

// UpsertSettings 管理员批量新增/更新系统设置。
func (h *SettingsHandler) UpsertSettings(c *gin.Context) {
	isAdmin, _ := c.Get(middleware.ContextIsAdminKey)
	if admin, ok := isAdmin.(bool); !ok || !admin {
		c.JSON(http.StatusForbidden, gin.H{"error": "admin only"})
		return
	}

	var req upsertSettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	if len(req.Items) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "items cannot be empty"})
		return
	}

	items := make([]service.SettingUpsertItem, 0, len(req.Items))
	for _, item := range req.Items {
		items = append(items, service.SettingUpsertItem{
			SettingKey:   item.SettingKey,
			SettingValue: item.SettingValue,
			Description:  item.Description,
		})
	}

	err := h.Svc.UpsertSettings(c.Request.Context(), items)
	if err != nil {
		if err == gorm.ErrInvalidData {
			c.JSON(http.StatusBadRequest, gin.H{"error": "setting_key and setting_value are required"})
			return
		}
		if err == gorm.ErrInvalidField {
			c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported setting_key"})
			return
		}
		if strings.Contains(err.Error(), "invalid value for") {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "save settings failed"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "settings updated"})
}

// GetPermissionSummary 聚合展示角色、空间角色与文件授权规则。
func (h *SettingsHandler) GetPermissionSummary(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)
	isAdmin, _ := c.Get(middleware.ContextIsAdminKey)
	isAdminBool, _ := isAdmin.(bool)

	all := strings.TrimSpace(c.Query("all")) == "1"
	if all && !isAdminBool {
		c.JSON(http.StatusForbidden, gin.H{"error": "admin only for all=1"})
		return
	}

	res, err := h.Svc.GetPermissionSummary(c.Request.Context(), uid, all)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"scope":            res.Scope,
		"roles":            res.Roles,
		"user_roles":       res.UserRoles,
		"workspace_roles":  res.WorkspaceRoles,
		"file_permissions": res.FilePermissions,
	})
}

// GetMyRateWindow 返回当前用户的高峰时段配置（仅时间窗口，不含速率值）。
func (h *SettingsHandler) GetMyRateWindow(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)
	res, err := h.Svc.GetMyRateWindow(c.Request.Context(), uid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"item": gin.H{
			"enabled": res.Enabled,
			"start":   res.Start,
			"end":     res.End,
			"source":  res.Source,
		},
	})
}

// UpdateMyRateWindow 更新当前用户的高峰时段配置。
func (h *SettingsHandler) UpdateMyRateWindow(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)

	var req updateRateWindowRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	start := strings.TrimSpace(req.Start)
	end := strings.TrimSpace(req.End)
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	if enabled {
		if !isValidHHMM(start) || !isValidHHMM(end) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "start/end should be HH:MM"})
			return
		}
	}

	err := h.Svc.UpdateMyRateWindow(c.Request.Context(), uid, enabled, start, end)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "save rate window failed"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "rate window updated"})
}

func isValidHHMM(v string) bool {
	v = strings.TrimSpace(v)
	if len(v) != 5 || v[2] != ':' || v[0] < '0' || v[0] > '9' || v[1] < '0' || v[1] > '9' || v[3] < '0' || v[3] > '9' || v[4] < '0' || v[4] > '9' {
		return false
	}
	h, errH := strconv.Atoi(v[:2])
	m, errM := strconv.Atoi(v[3:])
	if errH != nil || errM != nil {
		return false
	}
	return h >= 0 && h <= 23 && m >= 0 && m <= 59
}
