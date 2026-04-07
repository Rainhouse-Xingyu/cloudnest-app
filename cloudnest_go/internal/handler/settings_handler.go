package handler

import (
	"net/http"
	"strconv"
	"strings"

	"cloudenest_go/internal/middleware"
	"cloudenest_go/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// SettingsHandler 负责系统设置与权限聚合展示接口。
type SettingsHandler struct {
	DB *gorm.DB
}

type settingUpsertItem struct {
	SettingKey   string `json:"setting_key" binding:"required"`
	SettingValue string `json:"setting_value" binding:"required"`
	Description  string `json:"description"`
}

type upsertSettingsRequest struct {
	Items []settingUpsertItem `json:"items" binding:"required"`
}

type userRoleView struct {
	UserID   int64  `json:"user_id"`
	Username string `json:"username"`
	RoleID   int64  `json:"role_id"`
	RoleName string `json:"role_name"`
}

var allowedSystemSettingKeys = map[string]struct{}{
	"upload_chunk_threshold_mb":      {},
	"upload_default_chunk_size_mb":   {},
	"share_default_expire_hours":     {},
	"upload_speed_limit_kbps":        {},
	"download_speed_limit_kbps":      {},
	"upload_peak_speed_limit_kbps":   {},
	"download_peak_speed_limit_kbps": {},
	"peak_window_enabled":            {},
	"peak_window_start":              {},
	"peak_window_end":                {},
	"upload_max_concurrency":         {},
	"recycle_retention_days":         {},
}

type updateRateWindowRequest struct {
	Enabled *bool  `json:"enabled"`
	Start   string `json:"start"`
	End     string `json:"end"`
}

// GetSettings 返回 system_setting 全量配置。
func (h *SettingsHandler) GetSettings(c *gin.Context) {
	var rows []model.SystemSetting
	if err := h.DB.WithContext(c.Request.Context()).Order("setting_key ASC").Find(&rows).Error; err != nil {
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

	err := h.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		for _, item := range req.Items {
			key := strings.TrimSpace(item.SettingKey)
			value := strings.TrimSpace(item.SettingValue)
			desc := strings.TrimSpace(item.Description)
			if key == "" || value == "" {
				return gorm.ErrInvalidData
			}
			if _, ok := allowedSystemSettingKeys[key]; !ok {
				return gorm.ErrInvalidField
			}

			var row model.SystemSetting
			err := tx.Where("setting_key = ?", key).First(&row).Error
			if err != nil {
				if err != gorm.ErrRecordNotFound {
					return err
				}
				row = model.SystemSetting{SettingKey: key, SettingValue: value, Description: desc}
				if err := tx.Create(&row).Error; err != nil {
					return err
				}
				continue
			}

			updates := map[string]interface{}{"setting_value": value}
			if desc != "" {
				updates["description"] = desc
			}
			if err := tx.Model(&row).Updates(updates).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		if err == gorm.ErrInvalidData {
			c.JSON(http.StatusBadRequest, gin.H{"error": "setting_key and setting_value are required"})
			return
		}
		if err == gorm.ErrInvalidField {
			c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported setting_key"})
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

	var roles []model.Role
	if err := h.DB.WithContext(c.Request.Context()).Order("id ASC").Find(&roles).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}

	var roleRows []userRoleView
	roleSQL := h.DB.WithContext(c.Request.Context()).Table("user_role ur").
		Select("ur.user_id, u.username, ur.role_id, r.role_name").
		Joins("JOIN user u ON u.id = ur.user_id").
		Joins("JOIN role r ON r.id = ur.role_id")
	if !all {
		roleSQL = roleSQL.Where("ur.user_id = ?", uid)
	}
	if err := roleSQL.Order("ur.user_id ASC, ur.role_id ASC").Scan(&roleRows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}

	var workspaceRows []model.WorkspaceUser
	workspaceSQL := h.DB.WithContext(c.Request.Context()).Order("workspace_id ASC, user_id ASC")
	if !all {
		workspaceSQL = workspaceSQL.Where("user_id = ?", uid)
	}
	if err := workspaceSQL.Find(&workspaceRows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}

	var filePermRows []model.FilePermission
	filePermSQL := h.DB.WithContext(c.Request.Context()).Order("file_id ASC, user_id ASC")
	if !all {
		filePermSQL = filePermSQL.Where("user_id = ?", uid)
	}
	if err := filePermSQL.Find(&filePermRows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"scope": gin.H{
			"all":         all,
			"current_uid": uid,
		},
		"roles":            roles,
		"user_roles":       roleRows,
		"workspace_roles":  workspaceRows,
		"file_permissions": filePermRows,
	})
}

// GetMyRateWindow 返回当前用户的高峰时段配置（仅时间窗口，不含速率值）。
func (h *SettingsHandler) GetMyRateWindow(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)
	enabledKey := "peak_window_enabled_user_" + strconv.FormatInt(uid, 10)
	startKey := "peak_window_start_user_" + strconv.FormatInt(uid, 10)
	endKey := "peak_window_end_user_" + strconv.FormatInt(uid, 10)

	enabled, _ := h.readSettingValue(c, enabledKey)
	start, _ := h.readSettingValue(c, startKey)
	end, _ := h.readSettingValue(c, endKey)

	// 用户未配置时回落全局时段设置，保证可用性。
	source := "user"
	if enabled == "" && start == "" && end == "" {
		enabled, _ = h.readSettingValue(c, "peak_window_enabled")
		start, _ = h.readSettingValue(c, "peak_window_start")
		end, _ = h.readSettingValue(c, "peak_window_end")
		source = "global"
	}

	isEnabled := strings.TrimSpace(enabled) == "1"
	c.JSON(http.StatusOK, gin.H{
		"item": gin.H{
			"enabled": isEnabled,
			"start":   strings.TrimSpace(start),
			"end":     strings.TrimSpace(end),
			"source":  source,
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

	enabledKey := "peak_window_enabled_user_" + strconv.FormatInt(uid, 10)
	startKey := "peak_window_start_user_" + strconv.FormatInt(uid, 10)
	endKey := "peak_window_end_user_" + strconv.FormatInt(uid, 10)

	err := h.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if err := h.upsertSetting(tx, enabledKey, boolToFlag(enabled), "用户自定义高峰时段开关"); err != nil {
			return err
		}
		if enabled {
			if err := h.upsertSetting(tx, startKey, start, "用户自定义高峰开始时刻 HH:MM"); err != nil {
				return err
			}
			if err := h.upsertSetting(tx, endKey, end, "用户自定义高峰结束时刻 HH:MM"); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "save rate window failed"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "rate window updated"})
}

func (h *SettingsHandler) upsertSetting(tx *gorm.DB, key, value, desc string) error {
	var row model.SystemSetting
	err := tx.Where("setting_key = ?", key).First(&row).Error
	if err != nil {
		if err != gorm.ErrRecordNotFound {
			return err
		}
		row = model.SystemSetting{SettingKey: key, SettingValue: value, Description: desc}
		return tx.Create(&row).Error
	}
	return tx.Model(&row).Updates(map[string]interface{}{
		"setting_value": value,
		"description":   desc,
	}).Error
}

func (h *SettingsHandler) readSettingValue(c *gin.Context, key string) (string, error) {
	var row model.SystemSetting
	err := h.DB.WithContext(c.Request.Context()).Where("setting_key = ?", key).First(&row).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return "", nil
		}
		return "", err
	}
	return row.SettingValue, nil
}

func isValidHHMM(v string) bool {
	v = strings.TrimSpace(v)
	if len(v) != 5 || v[2] != ':' {
		return false
	}
	h, errH := strconv.Atoi(v[:2])
	m, errM := strconv.Atoi(v[3:])
	if errH != nil || errM != nil {
		return false
	}
	return h >= 0 && h <= 23 && m >= 0 && m <= 59
}

func boolToFlag(v bool) string {
	if v {
		return "1"
	}
	return "0"
}

func parseInt64Setting(v string) (int64, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}
