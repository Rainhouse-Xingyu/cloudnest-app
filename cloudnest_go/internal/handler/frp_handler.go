package handler

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"cloudenest_go/internal/middleware"
	"cloudenest_go/internal/service"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// FrpHandler 负责 FRP 设备与映射相关接口。
type FrpHandler struct {
	Svc *service.FrpService
}

// CreateDeviceRequest 创建设备请求。
type CreateDeviceRequest struct {
	DeviceName string `json:"device_name" binding:"required"`
	DeviceIP   string `json:"device_ip"`
}

// UpdateDeviceRequest 更新设备请求。
type UpdateDeviceRequest struct {
	DeviceName string `json:"device_name"`
	DeviceIP   string `json:"device_ip"`
	Status     *int8  `json:"status"`
}

// CreateMappingRequest 创建 FRP 映射请求。
type CreateMappingRequest struct {
	DeviceID   int64  `json:"device_id" binding:"required"`
	LocalPort  int    `json:"local_port" binding:"required"`
	RemotePort *int   `json:"remote_port"`
	Domain     string `json:"domain"`
	Status     *int8  `json:"status"`
}

// UpdateMappingRequest 更新 FRP 映射请求。
type UpdateMappingRequest struct {
	LocalPort  *int    `json:"local_port"`
	RemotePort *int    `json:"remote_port"`
	Domain     *string `json:"domain"`
	Status     *int8   `json:"status"`
}

// PreviewConfigRequest 预览 FRPC 配置请求。
type PreviewConfigRequest struct {
	DeviceID   int64  `json:"device_id" binding:"required"`
	ServerAddr string `json:"server_addr" binding:"required"`
	ServerPort int    `json:"server_port"`
	Token      string `json:"token"`
}

// SaveConfigRequest 保存 FRPC 配置请求。
type SaveConfigRequest struct {
	DeviceID   int64  `json:"device_id" binding:"required"`
	ServerAddr string `json:"server_addr" binding:"required"`
	ServerPort int    `json:"server_port"`
	Token      string `json:"token"`
}

// DeployConfigRequest 下发配置请求（预留）。
type DeployConfigRequest struct {
	ConfigID *int64 `json:"config_id"`
}

// ReportDeviceStatusRequest 设备状态上报请求。
type ReportDeviceStatusRequest struct {
	Online          bool   `json:"online"`
	LastHeartbeat   string `json:"last_heartbeat"`
	LastError       string `json:"last_error"`
	FailureReason   string `json:"failure_reason"`
	ClientVersion   string `json:"client_version"`
	CurrentConfigID *int64 `json:"current_config_id"`
}

// CommandResultRequest 命令执行结果上报请求。
type CommandResultRequest struct {
	Success       bool   `json:"success"`
	ResultMessage string `json:"result_message"`
	FailureReason string `json:"failure_reason"`
	ExecutedAt    string `json:"executed_at"`
}

// CreateDevice 创建设备并绑定当前用户。
func (h *FrpHandler) CreateDevice(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)

	var req CreateDeviceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	req.DeviceName = strings.TrimSpace(req.DeviceName)
	req.DeviceIP = strings.TrimSpace(req.DeviceIP)
	if req.DeviceName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "device_name is required"})
		return
	}

	device, err := h.Svc.CreateDevice(c.Request.Context(), uid, req.DeviceName, req.DeviceIP)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "create device failed"})
		return
	}

	_ = h.Svc.LogOperation(c.Request.Context(), uid, "frp_device_create", fmt.Sprintf("device_id=%d", device.ID), c.ClientIP())
	c.JSON(http.StatusOK, gin.H{"item": device})
}

// ListDevices 查询当前用户可见的设备；管理员可查看全部。
func (h *FrpHandler) ListDevices(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)
	isAdmin := h.isAdmin(c)

	statusText := strings.TrimSpace(c.Query("status"))
	var statusFilter *int8
	if statusText != "" {
		val, err := strconv.ParseInt(statusText, 10, 8)
		if err != nil || (val != 0 && val != 1) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid status"})
			return
		}
		t := int8(val)
		statusFilter = &t
	}

	rows, err := h.Svc.ListDevices(c.Request.Context(), uid, isAdmin, statusFilter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"items": rows})
}

// GetDevice 查询设备详情（含映射数量、最近配置时间）。
func (h *FrpHandler) GetDevice(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)
	deviceID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || deviceID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid device id"})
		return
	}

	res, err := h.Svc.GetDeviceDetail(c.Request.Context(), uid, h.isAdmin(c), deviceID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "device not found"})
			return
		}
		if errors.Is(err, service.ErrFrpForbidden) {
			c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"item": gin.H{
			"device":             res.Device,
			"mapping_count":      res.MappingCount,
			"latest_config_time": res.LatestConfigTime,
		},
	})
}

// UpdateDevice 更新设备名称、IP 和状态。
func (h *FrpHandler) UpdateDevice(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)
	deviceID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || deviceID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid device id"})
		return
	}

	var req UpdateDeviceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	device, err := h.Svc.UpdateDevice(c.Request.Context(), uid, h.isAdmin(c), deviceID, service.UpdateDeviceInput{
		DeviceName: req.DeviceName,
		DeviceIP:   req.DeviceIP,
		Status:     req.Status,
	})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "device not found"})
			return
		}
		if errors.Is(err, service.ErrFrpForbidden) {
			c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
			return
		}
		if errors.Is(err, gorm.ErrInvalidData) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "no fields to update"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	_ = h.Svc.LogOperation(c.Request.Context(), uid, "frp_device_update", fmt.Sprintf("device_id=%d", device.ID), c.ClientIP())
	c.JSON(http.StatusOK, gin.H{"item": device})
}

// DeleteDevice 删除设备及其关联映射（数据库已配置级联删除）。
func (h *FrpHandler) DeleteDevice(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)
	deviceID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || deviceID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid device id"})
		return
	}

	device, err := h.Svc.DeleteDevice(c.Request.Context(), uid, h.isAdmin(c), deviceID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "device not found"})
			return
		}
		if errors.Is(err, service.ErrFrpForbidden) {
			c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	_ = h.Svc.LogOperation(c.Request.Context(), uid, "frp_device_delete", fmt.Sprintf("device_id=%d", device.ID), c.ClientIP())
	c.JSON(http.StatusOK, gin.H{"message": "deleted"})
}

// CreateMapping 创建映射，支持自动分配 remote_port。
func (h *FrpHandler) CreateMapping(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)

	var req CreateMappingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	if req.LocalPort <= 0 || req.LocalPort > 65535 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid local_port"})
		return
	}
	if req.RemotePort != nil && (*req.RemotePort <= 0 || *req.RemotePort > 65535) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid remote_port"})
		return
	}
	req.Domain = strings.TrimSpace(req.Domain)

	created, allocErr := h.Svc.CreateMapping(c.Request.Context(), uid, h.isAdmin(c), service.CreateMappingInput{
		DeviceID:   req.DeviceID,
		LocalPort:  req.LocalPort,
		RemotePort: req.RemotePort,
		Domain:     req.Domain,
		Status:     req.Status,
	})
	if allocErr != nil {
		if errors.Is(allocErr, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "device not found"})
			return
		}
		if errors.Is(allocErr, service.ErrFrpForbidden) {
			c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
			return
		}
		msg := strings.ToLower(allocErr.Error())
		if strings.Contains(msg, "remote_port already exists") || strings.Contains(msg, "domain already exists") || strings.Contains(msg, "device disabled") {
			c.JSON(http.StatusConflict, gin.H{"error": allocErr.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "create mapping failed"})
		return
	}

	_ = h.Svc.LogOperation(c.Request.Context(), uid, "frp_mapping_create", fmt.Sprintf("mapping_id=%d device_id=%d", created.ID, created.DeviceID), c.ClientIP())
	c.JSON(http.StatusOK, gin.H{"item": created})
}

// ListDeviceMappings 查询某个设备下的映射列表。
func (h *FrpHandler) ListDeviceMappings(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)
	deviceID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || deviceID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid device id"})
		return
	}

	rows, err := h.Svc.ListDeviceMappings(c.Request.Context(), uid, h.isAdmin(c), deviceID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "device not found"})
			return
		}
		if errors.Is(err, service.ErrFrpForbidden) {
			c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"items": rows})
}

// UpdateMapping 更新映射信息（端口、域名、状态）。
func (h *FrpHandler) UpdateMapping(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)
	mappingID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || mappingID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid mapping id"})
		return
	}

	var req UpdateMappingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	mapping, err := h.Svc.UpdateMapping(c.Request.Context(), uid, h.isAdmin(c), mappingID, service.UpdateMappingInput{
		LocalPort:  req.LocalPort,
		RemotePort: req.RemotePort,
		Domain:     req.Domain,
		Status:     req.Status,
	})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "mapping not found"})
			return
		}
		if errors.Is(err, service.ErrFrpForbidden) {
			c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
			return
		}
		if errors.Is(err, gorm.ErrInvalidData) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "no fields to update"})
			return
		}
		msg := strings.ToLower(err.Error())
		if strings.Contains(msg, "already exists") || strings.Contains(msg, "device disabled") {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "update mapping failed"})
		return
	}
	_ = h.Svc.LogOperation(c.Request.Context(), uid, "frp_mapping_update", fmt.Sprintf("mapping_id=%d device_id=%d", mapping.ID, mapping.DeviceID), c.ClientIP())
	c.JSON(http.StatusOK, gin.H{"item": mapping})
}

// DeleteMapping 删除映射。
func (h *FrpHandler) DeleteMapping(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)
	mappingID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || mappingID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid mapping id"})
		return
	}

	mapping, err := h.Svc.DeleteMapping(c.Request.Context(), uid, h.isAdmin(c), mappingID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "mapping not found"})
			return
		}
		if errors.Is(err, service.ErrFrpForbidden) {
			c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	_ = h.Svc.LogOperation(c.Request.Context(), uid, "frp_mapping_delete", fmt.Sprintf("mapping_id=%d device_id=%d", mapping.ID, mapping.DeviceID), c.ClientIP())
	c.JSON(http.StatusOK, gin.H{"message": "deleted"})
}

// PreviewConfig 预览某设备的 frpc.yaml 内容（不入库）。
func (h *FrpHandler) PreviewConfig(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)

	var req PreviewConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	text, deviceID, err := h.Svc.PreviewConfig(c.Request.Context(), uid, h.isAdmin(c), service.SaveConfigInput{
		DeviceID:   req.DeviceID,
		ServerAddr: req.ServerAddr,
		ServerPort: req.ServerPort,
		Token:      req.Token,
	})
	if err != nil {
		h.writeConfigError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"device_id":   deviceID,
		"config_text": text,
	})
}

// SaveConfig 生成并保存设备配置到 frp_config 表。
func (h *FrpHandler) SaveConfig(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)

	var req SaveConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	cfg, err := h.Svc.SaveConfig(c.Request.Context(), uid, h.isAdmin(c), service.SaveConfigInput{
		DeviceID:   req.DeviceID,
		ServerAddr: req.ServerAddr,
		ServerPort: req.ServerPort,
		Token:      req.Token,
	})
	if err != nil {
		h.writeConfigError(c, err)
		return
	}

	if cfg.DeviceID == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "save config failed"})
		return
	}

	_ = h.Svc.LogOperation(c.Request.Context(), uid, "frp_config_save", fmt.Sprintf("config_id=%d device_id=%d", cfg.ID, *cfg.DeviceID), c.ClientIP())
	c.JSON(http.StatusOK, gin.H{"item": cfg})
}

// ListDeviceConfigs 查询设备配置历史（按时间倒序）。
func (h *FrpHandler) ListDeviceConfigs(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)
	deviceID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || deviceID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid device id"})
		return
	}

	rows, err := h.Svc.ListDeviceConfigs(c.Request.Context(), uid, h.isAdmin(c), deviceID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "device not found"})
			return
		}
		if errors.Is(err, service.ErrFrpForbidden) {
			c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"items": rows})
}

// RollbackConfig 将历史配置复制为最新版本，实现回滚。
func (h *FrpHandler) RollbackConfig(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)
	configID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || configID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid config id"})
		return
	}

	oldCfg, newCfg, err := h.Svc.RollbackConfig(c.Request.Context(), uid, h.isAdmin(c), configID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// 与原逻辑一致：优先返回配置不存在。
			c.JSON(http.StatusNotFound, gin.H{"error": "config not found"})
			return
		}
		if errors.Is(err, service.ErrFrpForbidden) {
			c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
			return
		}
		if strings.Contains(strings.ToLower(err.Error()), "config has no device") {
			c.JSON(http.StatusConflict, gin.H{"error": "config has no device"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if oldCfg.DeviceID == nil {
		c.JSON(http.StatusConflict, gin.H{"error": "config has no device"})
		return
	}

	// 保持原返回和日志结构不变。
	_ = h.Svc.LogOperation(c.Request.Context(), uid, "frp_config_rollback", fmt.Sprintf("from_config_id=%d to_config_id=%d device_id=%d", oldCfg.ID, newCfg.ID, *oldCfg.DeviceID), c.ClientIP())
	c.JSON(http.StatusOK, gin.H{"item": newCfg})
}

// DeployConfig 下发配置接口（服务端预留：写入命令队列，等待客户端执行）。
func (h *FrpHandler) DeployConfig(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)
	deviceID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || deviceID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid device id"})
		return
	}

	var req DeployConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	commandID, configID, err := h.Svc.DeployConfig(c.Request.Context(), uid, h.isAdmin(c), deviceID, req.ConfigID)
	if err != nil {
		if errors.Is(err, service.ErrFrpNoConfigForDevice) {
			c.JSON(http.StatusConflict, gin.H{"error": "no config for device"})
			return
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "config not found"})
			return
		}
		if errors.Is(err, service.ErrFrpForbidden) {
			c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
			return
		}
		if strings.Contains(strings.ToLower(err.Error()), "device not found") {
			c.JSON(http.StatusNotFound, gin.H{"error": "device not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "enqueue command failed"})
		return
	}
	_ = h.Svc.LogOperation(c.Request.Context(), uid, "frp_config_deploy", fmt.Sprintf("device_id=%d config_id=%d command_id=%s", deviceID, configID, commandID), c.ClientIP())
	c.JSON(http.StatusOK, gin.H{
		"message":    "deploy command queued",
		"command_id": commandID,
		"device_id":  deviceID,
		"config_id":  configID,
	})
}

// ReloadDevice 下发重载命令接口（服务端预留：写入命令队列，等待客户端执行）。
func (h *FrpHandler) ReloadDevice(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)
	deviceID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || deviceID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid device id"})
		return
	}

	commandID, err := h.Svc.ReloadDevice(c.Request.Context(), uid, h.isAdmin(c), deviceID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "device not found"})
			return
		}
		if errors.Is(err, service.ErrFrpForbidden) {
			c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "enqueue command failed"})
		return
	}
	_ = h.Svc.LogOperation(c.Request.Context(), uid, "frp_reload", fmt.Sprintf("device_id=%d command_id=%s", deviceID, commandID), c.ClientIP())
	c.JSON(http.StatusOK, gin.H{
		"message":    "reload command queued",
		"command_id": commandID,
		"device_id":  deviceID,
	})
}

// ReportDeviceStatus 设备上报在线状态、心跳和失败原因。
func (h *FrpHandler) ReportDeviceStatus(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)
	deviceID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || deviceID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid device id"})
		return
	}

	var req ReportDeviceStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	err = h.Svc.ReportDeviceStatus(c.Request.Context(), uid, h.isAdmin(c), deviceID, service.ReportDeviceStatusInput{
		Online:          req.Online,
		LastHeartbeat:   req.LastHeartbeat,
		LastError:       req.LastError,
		FailureReason:   req.FailureReason,
		ClientVersion:   req.ClientVersion,
		CurrentConfigID: req.CurrentConfigID,
	})
	if err != nil {
		if errors.Is(err, service.ErrFrpInvalidHeartbeat) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "last_heartbeat must be RFC3339"})
			return
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "device not found"})
			return
		}
		if errors.Is(err, service.ErrFrpForbidden) {
			c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "redis error"})
		return
	}

	_ = h.Svc.LogOperation(c.Request.Context(), uid, "frp_status_report", fmt.Sprintf("device_id=%d online=%v", deviceID, req.Online), c.ClientIP())
	c.JSON(http.StatusOK, gin.H{"message": "status updated"})
}

// GetDeviceStatus 查询设备最近状态（在线、最近心跳、失败原因）。
func (h *FrpHandler) GetDeviceStatus(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)
	deviceID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || deviceID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid device id"})
		return
	}

	item, err := h.Svc.GetDeviceStatus(c.Request.Context(), uid, h.isAdmin(c), deviceID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "device not found"})
			return
		}
		if errors.Is(err, service.ErrFrpForbidden) {
			c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "redis error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"item": gin.H{
			"online":            item.Online,
			"last_heartbeat":    item.LastHeartbeat,
			"last_error":        item.LastError,
			"client_version":    item.ClientVersion,
			"updated_at":        item.UpdatedAt,
			"current_config_id": item.CurrentConfigID,
		},
	})
}

// PullNextCommand 客户端拉取下一条待执行命令。
func (h *FrpHandler) PullNextCommand(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)
	deviceID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || deviceID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid device id"})
		return
	}

	item, err := h.Svc.PullNextCommand(c.Request.Context(), uid, h.isAdmin(c), deviceID)
	if err != nil {
		if errors.Is(err, service.ErrFrpInvalidQueuedCommand) {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "invalid queued command"})
			return
		}
		if errors.Is(err, service.ErrFrpCommandDataNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "command data not found"})
			return
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "device not found"})
			return
		}
		if errors.Is(err, service.ErrFrpForbidden) {
			c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "redis error"})
		return
	}
	if item == nil {
		c.JSON(http.StatusOK, gin.H{"item": nil})
		return
	}

	_ = h.Svc.LogOperation(c.Request.Context(), uid, "frp_command_pull", fmt.Sprintf("device_id=%d command_id=%s", deviceID, item.CommandID), c.ClientIP())
	c.JSON(http.StatusOK, gin.H{
		"item": gin.H{
			"command_id":   item.CommandID,
			"action":       item.Action,
			"config_id":    item.ConfigID,
			"status":       item.Status,
			"created_at":   item.CreatedAt,
			"delivered_at": item.DeliveredAt,
			"payload":      item.Payload,
		},
	})
}

// ReportCommandResult 客户端回传命令执行结果。
func (h *FrpHandler) ReportCommandResult(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)
	commandID := strings.TrimSpace(c.Param("command_id"))
	if commandID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid command id"})
		return
	}

	var req CommandResultRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	deviceID, status, err := h.Svc.ReportCommandResult(c.Request.Context(), uid, h.isAdmin(c), commandID, service.CommandResultInput{
		Success:       req.Success,
		ResultMessage: req.ResultMessage,
		FailureReason: req.FailureReason,
		ExecutedAt:    req.ExecutedAt,
	})
	if err != nil {
		if errors.Is(err, service.ErrFrpCommandNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "command not found"})
			return
		}
		if errors.Is(err, service.ErrFrpInvalidCommandData) {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "invalid command data"})
			return
		}
		if errors.Is(err, service.ErrFrpInvalidExecutedAt) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "executed_at must be RFC3339"})
			return
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "device not found"})
			return
		}
		if errors.Is(err, service.ErrFrpForbidden) {
			c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "redis error"})
		return
	}

	_ = h.Svc.LogOperation(c.Request.Context(), uid, "frp_command_result", fmt.Sprintf("command_id=%s device_id=%d status=%s", commandID, deviceID, status), c.ClientIP())
	c.JSON(http.StatusOK, gin.H{"message": "command result updated"})
}

// ListDeviceCommands 管理端查询设备命令历史与状态。
func (h *FrpHandler) ListDeviceCommands(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)
	deviceID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || deviceID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid device id"})
		return
	}

	limit, err := parseLimitWithDefault(c.Query("limit"), 20, 100)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid limit"})
		return
	}

	rows, err := h.Svc.ListDeviceCommands(c.Request.Context(), uid, h.isAdmin(c), deviceID, limit)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "device not found"})
			return
		}
		if errors.Is(err, service.ErrFrpForbidden) {
			c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "redis error"})
		return
	}

	items := make([]gin.H, 0, len(rows))
	for _, row := range rows {
		items = append(items, gin.H{
			"command_id":     row["command_id"],
			"action":         row["action"],
			"status":         row["status"],
			"device_id":      row["device_id"],
			"config_id":      row["config_id"],
			"created_at":     row["created_at"],
			"delivered_at":   row["delivered_at"],
			"executed_at":    row["executed_at"],
			"result_message": row["result_message"],
			"failure_reason": row["failure_reason"],
			"updated_at":     row["updated_at"],
		})
	}

	c.JSON(http.StatusOK, gin.H{"items": items})
}

// isAdmin 判断当前登录用户是否管理员。
func (h *FrpHandler) isAdmin(c *gin.Context) bool {
	isAdminAny, ok := c.Get(middleware.ContextIsAdminKey)
	if !ok {
		return false
	}
	isAdmin, ok := isAdminAny.(bool)
	return ok && isAdmin
}

// parseLimitWithDefault 解析分页 limit，包含默认值和最大值限制。
func parseLimitWithDefault(raw string, defaultVal int, maxVal int) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultVal, nil
	}
	val, err := strconv.Atoi(raw)
	if err != nil || val <= 0 {
		return 0, fmt.Errorf("invalid limit")
	}
	if val > maxVal {
		val = maxVal
	}
	return val, nil
}

// writeConfigError 统一处理配置相关错误码。
func (h *FrpHandler) writeConfigError(c *gin.Context, err error) {
	if err == nil {
		return
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "device not found"})
		return
	}
	if errors.Is(err, service.ErrFrpForbidden) {
		c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
		return
	}
	if errors.Is(err, service.ErrFrpNoMapping) {
		c.JSON(http.StatusConflict, gin.H{"error": "no active mapping"})
		return
	}
	if errors.Is(err, service.ErrFrpNeedServer) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "server_addr is required"})
		return
	}
	if errors.Is(err, service.ErrFrpBadConfig) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid config parameters"})
		return
	}
	c.JSON(http.StatusInternalServerError, gin.H{"error": "build config failed"})
}
