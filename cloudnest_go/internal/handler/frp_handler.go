package handler

import (
	"encoding/json"
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
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

const (
	// frp默认远程端口池范围，可按后续配置中心改为动态读取
	defaultFrpRemotePortStart = 20000
	defaultFrpRemotePortEnd   = 20999
	maxAllocateRetries        = 5
	frpStatusKeyPrefix        = "frp:device:status:"
	frpCommandQueuePrefix     = "frp:device:cmd:"
	frpLastCommandKeyPrefix   = "frp:device:last_command:"
	frpCommandDataKeyPrefix   = "frp:command:data:"
	frpCommandHistoryPrefix   = "frp:device:command:history:"
	frpCommandHistoryLimit    = 200
)

// FrpHandler 负责 FRP 设备与映射相关接口。
type FrpHandler struct {
	DB  *gorm.DB
	RDB *redis.Client
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

	_, err = h.findDeviceWithPermission(c, uid, deviceID)
	if err != nil {
		h.writeConfigError(c, err)
		return
	}

	configID, err := h.resolveConfigID(c, deviceID, req.ConfigID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "config not found"})
			return
		}
		c.JSON(http.StatusConflict, gin.H{"error": "no config for device"})
		return
	}

	commandID := fmt.Sprintf("deploy-%d-%d", deviceID, time.Now().UnixNano())
	payload := map[string]interface{}{
		"command_id": commandID,
		"action":     "deploy_config",
		"device_id":  deviceID,
		"config_id":  configID,
		"created_at": time.Now().Format(time.RFC3339),
	}
	if err := h.enqueueDeviceCommand(c, deviceID, payload); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "enqueue command failed"})
		return
	}

	_ = h.logFrpOperation(c, uid, "frp_config_deploy", fmt.Sprintf("device_id=%d config_id=%d command_id=%s", deviceID, configID, commandID))
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

	_, err = h.findDeviceWithPermission(c, uid, deviceID)
	if err != nil {
		h.writeConfigError(c, err)
		return
	}

	commandID := fmt.Sprintf("reload-%d-%d", deviceID, time.Now().UnixNano())
	payload := map[string]interface{}{
		"command_id": commandID,
		"action":     "reload",
		"device_id":  deviceID,
		"created_at": time.Now().Format(time.RFC3339),
	}
	if err := h.enqueueDeviceCommand(c, deviceID, payload); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "enqueue command failed"})
		return
	}

	_ = h.logFrpOperation(c, uid, "frp_reload", fmt.Sprintf("device_id=%d command_id=%s", deviceID, commandID))
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

	_, err = h.findDeviceWithPermission(c, uid, deviceID)
	if err != nil {
		h.writeConfigError(c, err)
		return
	}

	var req ReportDeviceStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	heartbeatAt := time.Now()
	if strings.TrimSpace(req.LastHeartbeat) != "" {
		parsed, parseErr := time.Parse(time.RFC3339, strings.TrimSpace(req.LastHeartbeat))
		if parseErr != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "last_heartbeat must be RFC3339"})
			return
		}
		heartbeatAt = parsed
	}

	lastError := strings.TrimSpace(req.LastError)
	if strings.TrimSpace(req.FailureReason) != "" {
		lastError = strings.TrimSpace(req.FailureReason)
	}
	if len(lastError) > 500 {
		lastError = lastError[:500]
	}

	statusKey := frpStatusKeyPrefix + strconv.FormatInt(deviceID, 10)
	values := map[string]interface{}{
		"online":            boolToInt(req.Online),
		"last_heartbeat":    heartbeatAt.Format(time.RFC3339),
		"last_error":        lastError,
		"client_version":    strings.TrimSpace(req.ClientVersion),
		"updated_at":        time.Now().Format(time.RFC3339),
		"current_config_id": 0,
	}
	if req.CurrentConfigID != nil && *req.CurrentConfigID > 0 {
		values["current_config_id"] = *req.CurrentConfigID
	}

	if err := h.RDB.HSet(c.Request.Context(), statusKey, values).Err(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "redis error"})
		return
	}
	_ = h.RDB.Expire(c.Request.Context(), statusKey, 7*24*time.Hour).Err()

	_ = h.logFrpOperation(c, uid, "frp_status_report", fmt.Sprintf("device_id=%d online=%v", deviceID, req.Online))
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

	_, err = h.findDeviceWithPermission(c, uid, deviceID)
	if err != nil {
		h.writeConfigError(c, err)
		return
	}

	statusKey := frpStatusKeyPrefix + strconv.FormatInt(deviceID, 10)
	data, err := h.RDB.HGetAll(c.Request.Context(), statusKey).Result()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "redis error"})
		return
	}
	if len(data) == 0 {
		c.JSON(http.StatusOK, gin.H{
			"item": gin.H{
				"online":         false,
				"last_heartbeat": nil,
				"last_error":     "",
				"client_version": "",
			},
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"item": gin.H{
			"online":            data["online"] == "1",
			"last_heartbeat":    data["last_heartbeat"],
			"last_error":        data["last_error"],
			"client_version":    data["client_version"],
			"updated_at":        data["updated_at"],
			"current_config_id": data["current_config_id"],
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

	_, err = h.findDeviceWithPermission(c, uid, deviceID)
	if err != nil {
		h.writeConfigError(c, err)
		return
	}

	queueKey := frpCommandQueuePrefix + strconv.FormatInt(deviceID, 10)
	rawToken, err := h.RDB.LPop(c.Request.Context(), queueKey).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			c.JSON(http.StatusOK, gin.H{"item": nil})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "redis error"})
		return
	}

	commandID := strings.TrimSpace(rawToken)
	if strings.HasPrefix(commandID, "{") {
		// 兼容早期直接将整段 JSON 入队的情况。
		legacyID, convertErr := h.convertLegacyQueuedCommand(c, deviceID, commandID)
		if convertErr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "invalid queued command"})
			return
		}
		commandID = legacyID
	}

	dataKey := frpCommandDataKeyPrefix + commandID
	cmdData, err := h.RDB.HGetAll(c.Request.Context(), dataKey).Result()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "redis error"})
		return
	}
	if len(cmdData) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "command data not found"})
		return
	}

	now := time.Now().Format(time.RFC3339)
	_ = h.RDB.HSet(c.Request.Context(), dataKey, map[string]interface{}{
		"status":       "delivered",
		"delivered_at": now,
		"updated_at":   now,
	}).Err()

	_ = h.logFrpOperation(c, uid, "frp_command_pull", fmt.Sprintf("device_id=%d command_id=%s", deviceID, commandID))
	c.JSON(http.StatusOK, gin.H{
		"item": gin.H{
			"command_id":   commandID,
			"action":       cmdData["action"],
			"config_id":    cmdData["config_id"],
			"status":       "delivered",
			"created_at":   cmdData["created_at"],
			"delivered_at": now,
			"payload":      cmdData["payload"],
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

	dataKey := frpCommandDataKeyPrefix + commandID
	cmdData, err := h.RDB.HGetAll(c.Request.Context(), dataKey).Result()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "redis error"})
		return
	}
	if len(cmdData) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "command not found"})
		return
	}

	deviceID, parseErr := strconv.ParseInt(cmdData["device_id"], 10, 64)
	if parseErr != nil || deviceID <= 0 {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "invalid command data"})
		return
	}

	_, err = h.findDeviceWithPermission(c, uid, deviceID)
	if err != nil {
		h.writeConfigError(c, err)
		return
	}

	executedAt := time.Now()
	if strings.TrimSpace(req.ExecutedAt) != "" {
		parsed, pErr := time.Parse(time.RFC3339, strings.TrimSpace(req.ExecutedAt))
		if pErr != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "executed_at must be RFC3339"})
			return
		}
		executedAt = parsed
	}

	msg := strings.TrimSpace(req.ResultMessage)
	if len(msg) > 500 {
		msg = msg[:500]
	}
	failureReason := strings.TrimSpace(req.FailureReason)
	if len(failureReason) > 500 {
		failureReason = failureReason[:500]
	}

	status := "executed_success"
	if !req.Success {
		status = "executed_failed"
		if failureReason == "" {
			failureReason = msg
		}
	}

	if err := h.RDB.HSet(c.Request.Context(), dataKey, map[string]interface{}{
		"status":         status,
		"result_message": msg,
		"failure_reason": failureReason,
		"executed_at":    executedAt.Format(time.RFC3339),
		"updated_at":     time.Now().Format(time.RFC3339),
	}).Err(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "redis error"})
		return
	}

	_ = h.logFrpOperation(c, uid, "frp_command_result", fmt.Sprintf("command_id=%s device_id=%d status=%s", commandID, deviceID, status))
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

	_, err = h.findDeviceWithPermission(c, uid, deviceID)
	if err != nil {
		h.writeConfigError(c, err)
		return
	}

	limit, err := parseLimitWithDefault(c.Query("limit"), 20, 100)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid limit"})
		return
	}

	historyKey := frpCommandHistoryPrefix + strconv.FormatInt(deviceID, 10)
	ids, err := h.RDB.LRange(c.Request.Context(), historyKey, 0, int64(limit-1)).Result()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "redis error"})
		return
	}

	items := make([]gin.H, 0, len(ids))
	for _, commandID := range ids {
		dataKey := frpCommandDataKeyPrefix + strings.TrimSpace(commandID)
		row, hErr := h.RDB.HGetAll(c.Request.Context(), dataKey).Result()
		if hErr != nil || len(row) == 0 {
			continue
		}
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

var errFrpForbidden = errors.New("frp permission denied")

// findDeviceWithPermission 查询设备并校验当前用户是否有权限访问。
func (h *FrpHandler) findDeviceWithPermission(c *gin.Context, userID int64, deviceID int64) (model.Device, error) {
	var device model.Device
	if err := h.DB.WithContext(c.Request.Context()).First(&device, "id = ?", deviceID).Error; err != nil {
		return model.Device{}, err
	}
	if h.isAdmin(c) || device.UserID == userID {
		return device, nil
	}
	return model.Device{}, errFrpForbidden
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

// allocateRemotePort 在固定端口池中分配可用端口。
func (h *FrpHandler) allocateRemotePort(tx *gorm.DB) (int, error) {
	for i := 0; i < maxAllocateRetries; i++ {
		var maxUsed int
		if err := tx.Model(&model.FrpMapping{}).
			Where("remote_port BETWEEN ? AND ?", defaultFrpRemotePortStart, defaultFrpRemotePortEnd).
			Select("COALESCE(MAX(remote_port), 0)").
			Scan(&maxUsed).Error; err != nil {
			return 0, err
		}

		candidate := defaultFrpRemotePortStart
		if maxUsed >= defaultFrpRemotePortStart && maxUsed < defaultFrpRemotePortEnd {
			candidate = maxUsed + 1
		}

		// 如果尾部端口已满，则从起始端口寻找空洞。
		if maxUsed >= defaultFrpRemotePortEnd {
			for p := defaultFrpRemotePortStart; p <= defaultFrpRemotePortEnd; p++ {
				var count int64
				if err := tx.Model(&model.FrpMapping{}).Where("remote_port = ?", p).Count(&count).Error; err != nil {
					return 0, err
				}
				if count == 0 {
					candidate = p
					break
				}
			}
		}

		if candidate < defaultFrpRemotePortStart || candidate > defaultFrpRemotePortEnd {
			return 0, fmt.Errorf("remote port pool exhausted")
		}

		var occupied int64
		if err := tx.Model(&model.FrpMapping{}).Where("remote_port = ?", candidate).Count(&occupied).Error; err != nil {
			return 0, err
		}
		if occupied == 0 {
			return candidate, nil
		}
	}

	return 0, fmt.Errorf("allocate remote port failed after retries")
}

// isDuplicateKeyErr 判断是否为唯一键冲突。
func isDuplicateKeyErr(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "duplicate") || strings.Contains(text, "1062")
}

var (
	errFrpNoMapping  = errors.New("frp mapping not found")
	errFrpBadConfig  = errors.New("frp config invalid")
	errFrpNeedServer = errors.New("frp server_addr is required")
)

// findMappingWithPermission 查询映射并校验当前用户权限。
func (h *FrpHandler) findMappingWithPermission(c *gin.Context, userID int64, mappingID int64) (model.FrpMapping, model.Device, error) {
	var mapping model.FrpMapping
	if err := h.DB.WithContext(c.Request.Context()).First(&mapping, "id = ?", mappingID).Error; err != nil {
		return model.FrpMapping{}, model.Device{}, err
	}
	device, err := h.findDeviceWithPermission(c, userID, mapping.DeviceID)
	if err != nil {
		return model.FrpMapping{}, model.Device{}, err
	}
	return mapping, device, nil
}

// resolveConfigID 解析要下发的配置 ID；如果请求未指定则选择设备最新配置。
func (h *FrpHandler) resolveConfigID(c *gin.Context, deviceID int64, requested *int64) (int64, error) {
	var cfg model.FrpConfig
	query := h.DB.WithContext(c.Request.Context()).Where("device_id = ?", deviceID)
	if requested != nil && *requested > 0 {
		query = query.Where("id = ?", *requested)
	}
	if err := query.Order("id DESC").First(&cfg).Error; err != nil {
		return 0, err
	}
	return cfg.ID, nil
}

// enqueueDeviceCommand 将命令写入设备队列，供客户端轮询拉取后执行。
func (h *FrpHandler) enqueueDeviceCommand(c *gin.Context, deviceID int64, payload map[string]interface{}) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	commandID := strings.TrimSpace(fmt.Sprintf("%v", payload["command_id"]))
	if commandID == "" || commandID == "<nil>" {
		return fmt.Errorf("missing command_id")
	}
	action := strings.TrimSpace(fmt.Sprintf("%v", payload["action"]))
	if action == "" || action == "<nil>" {
		action = "unknown"
	}
	configID := int64(0)
	if val, ok := payload["config_id"]; ok {
		configID = parseAnyToInt64(val)
	}
	now := time.Now().Format(time.RFC3339)

	queueKey := frpCommandQueuePrefix + strconv.FormatInt(deviceID, 10)
	lastKey := frpLastCommandKeyPrefix + strconv.FormatInt(deviceID, 10)
	historyKey := frpCommandHistoryPrefix + strconv.FormatInt(deviceID, 10)
	if err := h.persistCommandData(c, commandID, deviceID, action, configID, "pending", string(raw), now); err != nil {
		return err
	}
	if err := h.RDB.RPush(c.Request.Context(), queueKey, commandID).Err(); err != nil {
		return err
	}
	if err := h.RDB.Expire(c.Request.Context(), queueKey, 7*24*time.Hour).Err(); err != nil {
		return err
	}
	if err := h.RDB.LPush(c.Request.Context(), historyKey, commandID).Err(); err != nil {
		return err
	}
	if err := h.RDB.LTrim(c.Request.Context(), historyKey, 0, frpCommandHistoryLimit-1).Err(); err != nil {
		return err
	}
	if err := h.RDB.Expire(c.Request.Context(), historyKey, 7*24*time.Hour).Err(); err != nil {
		return err
	}
	if err := h.RDB.Set(c.Request.Context(), lastKey, string(raw), 7*24*time.Hour).Err(); err != nil {
		return err
	}
	return nil
}

// boolToInt 布尔值转整型，便于 Redis 字段存储。
func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// convertLegacyQueuedCommand 兼容旧队列格式（整段 JSON），并转换为标准命令数据。
func (h *FrpHandler) convertLegacyQueuedCommand(c *gin.Context, deviceID int64, raw string) (string, error) {
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return "", err
	}
	commandID := strings.TrimSpace(fmt.Sprintf("%v", payload["command_id"]))
	if commandID == "" || commandID == "<nil>" {
		commandID = fmt.Sprintf("legacy-%d-%d", deviceID, time.Now().UnixNano())
		payload["command_id"] = commandID
	}
	if _, ok := payload["device_id"]; !ok {
		payload["device_id"] = deviceID
	}
	action := strings.TrimSpace(fmt.Sprintf("%v", payload["action"]))
	if action == "" || action == "<nil>" {
		action = "unknown"
	}
	configID := int64(0)
	if val, ok := payload["config_id"]; ok {
		configID = parseAnyToInt64(val)
	}
	now := time.Now().Format(time.RFC3339)
	if err := h.persistCommandData(c, commandID, deviceID, action, configID, "pending", raw, now); err != nil {
		return "", err
	}
	historyKey := frpCommandHistoryPrefix + strconv.FormatInt(deviceID, 10)
	if err := h.RDB.LPush(c.Request.Context(), historyKey, commandID).Err(); err != nil {
		return "", err
	}
	if err := h.RDB.LTrim(c.Request.Context(), historyKey, 0, frpCommandHistoryLimit-1).Err(); err != nil {
		return "", err
	}
	if err := h.RDB.Expire(c.Request.Context(), historyKey, 7*24*time.Hour).Err(); err != nil {
		return "", err
	}
	return commandID, nil
}

// persistCommandData 持久化命令基础信息，用于后续状态流转和历史查询。
func (h *FrpHandler) persistCommandData(c *gin.Context, commandID string, deviceID int64, action string, configID int64, status string, payload string, now string) error {
	commandDataKey := frpCommandDataKeyPrefix + commandID
	if err := h.RDB.HSet(c.Request.Context(), commandDataKey, map[string]interface{}{
		"command_id":     commandID,
		"action":         action,
		"device_id":      deviceID,
		"config_id":      configID,
		"status":         status,
		"created_at":     now,
		"updated_at":     now,
		"payload":        payload,
		"result_message": "",
		"failure_reason": "",
	}).Err(); err != nil {
		return err
	}
	if err := h.RDB.Expire(c.Request.Context(), commandDataKey, 7*24*time.Hour).Err(); err != nil {
		return err
	}
	return nil
}

// parseAnyToInt64 将任意常见数字类型转换为 int64。
func parseAnyToInt64(v interface{}) int64 {
	s := strings.TrimSpace(fmt.Sprintf("%v", v))
	if s == "" || s == "<nil>" {
		return 0
	}
	i, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return i
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

// buildConfigTextForDevice 根据设备和映射生成 frpc.yaml 文本。
func (h *FrpHandler) buildConfigTextForDevice(c *gin.Context, userID int64, deviceID int64, serverAddr string, serverPort int, token string) (string, int64, error) {
	if deviceID <= 0 {
		return "", 0, errFrpBadConfig
	}

	_, err := h.findDeviceWithPermission(c, userID, deviceID)
	if err != nil {
		return "", 0, err
	}

	serverAddr = strings.TrimSpace(serverAddr)
	if serverAddr == "" {
		return "", 0, errFrpNeedServer
	}
	if serverPort <= 0 {
		serverPort = 7000
	}
	if serverPort > 65535 {
		return "", 0, errFrpBadConfig
	}

	var mappings []model.FrpMapping
	if err := h.DB.WithContext(c.Request.Context()).Where("device_id = ? AND status = 1", deviceID).Order("id ASC").Find(&mappings).Error; err != nil {
		return "", 0, err
	}
	if len(mappings) == 0 {
		return "", 0, errFrpNoMapping
	}

	text := generateFRPCYaml(serverAddr, serverPort, token, mappings)
	return text, deviceID, nil
}

// generateFRPCYaml 生成基础 frpc.yaml 文本，支持 tcp/http/https 三类代理。
func generateFRPCYaml(serverAddr string, serverPort int, token string, mappings []model.FrpMapping) string {
	b := strings.Builder{}
	b.WriteString("serverAddr: ")
	b.WriteString(serverAddr)
	b.WriteString("\n")
	b.WriteString("serverPort: ")
	b.WriteString(strconv.Itoa(serverPort))
	b.WriteString("\n")
	if strings.TrimSpace(token) != "" {
		b.WriteString("auth:\n")
		b.WriteString("  token: ")
		b.WriteString(token)
		b.WriteString("\n")
	}
	b.WriteString("proxies:\n")

	for _, m := range mappings {
		proxyName := fmt.Sprintf("device_%d_map_%d", m.DeviceID, m.ID)
		typeName := "tcp"
		if m.Domain != "" {
			if strings.HasPrefix(m.Domain, "https://") {
				typeName = "https"
			} else {
				typeName = "http"
			}
		}

		b.WriteString("  - name: ")
		b.WriteString(proxyName)
		b.WriteString("\n")
		b.WriteString("    type: ")
		b.WriteString(typeName)
		b.WriteString("\n")
		b.WriteString("    localPort: ")
		b.WriteString(strconv.Itoa(m.LocalPort))
		b.WriteString("\n")

		if typeName == "tcp" {
			b.WriteString("    remotePort: ")
			b.WriteString(strconv.Itoa(m.RemotePort))
			b.WriteString("\n")
		} else {
			domain := strings.TrimPrefix(strings.TrimPrefix(m.Domain, "https://"), "http://")
			b.WriteString("    customDomains:\n")
			b.WriteString("      - ")
			b.WriteString(domain)
			b.WriteString("\n")
		}
	}

	return b.String()
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
	if err == errFrpForbidden {
		c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
		return
	}
	if err == errFrpNoMapping {
		c.JSON(http.StatusConflict, gin.H{"error": "no active mapping"})
		return
	}
	if err == errFrpNeedServer {
		c.JSON(http.StatusBadRequest, gin.H{"error": "server_addr is required"})
		return
	}
	if err == errFrpBadConfig {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid config parameters"})
		return
	}
	c.JSON(http.StatusInternalServerError, gin.H{"error": "build config failed"})
}

// logFrpOperation 写入 FRP 关键操作日志。
func (h *FrpHandler) logFrpOperation(c *gin.Context, userID int64, action string, detail string) error {
	if len(detail) > 500 {
		detail = detail[:500]
	}
	uid := userID
	return h.DB.WithContext(c.Request.Context()).Create(&model.OperationLog{
		UserID:   &uid,
		Action:   action,
		Status:   "success",
		Detail:   detail,
		ClientIP: c.ClientIP(),
	}).Error
}
