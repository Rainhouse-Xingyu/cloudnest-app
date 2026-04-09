package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"cloudenest_go/internal/service/contract"
	"cloudenest_go/model"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

const (
	frpStatusKeyPrefix      = "frp:device:status:"
	frpCommandQueuePrefix   = "frp:device:cmd:"
	frpLastCommandKeyPrefix = "frp:device:last_command:"
	frpCommandDataKeyPrefix = "frp:command:data:"
	frpCommandHistoryPrefix = "frp:device:command:history:"
	frpCommandHistoryLimit  = 200
)

var (
	// ErrFrpForbidden 表示当前用户无权限访问目标资源。
	ErrFrpForbidden = errors.New("frp permission denied")
	// ErrFrpNoMapping 表示设备没有可用映射。
	ErrFrpNoMapping = errors.New("frp mapping not found")
	// ErrFrpBadConfig 表示配置参数不合法。
	ErrFrpBadConfig = errors.New("frp config invalid")
	// ErrFrpNeedServer 表示缺少服务端地址。
	ErrFrpNeedServer = errors.New("frp server_addr is required")
	// ErrFrpNoConfigForDevice 表示设备不存在可下发配置。
	ErrFrpNoConfigForDevice = errors.New("frp no config for device")
	// ErrFrpInvalidQueuedCommand 表示命令队列中的旧数据格式无效。
	ErrFrpInvalidQueuedCommand = errors.New("frp invalid queued command")
	// ErrFrpCommandDataNotFound 表示命令数据不存在。
	ErrFrpCommandDataNotFound = errors.New("frp command data not found")
	// ErrFrpCommandNotFound 表示命令不存在。
	ErrFrpCommandNotFound = errors.New("frp command not found")
	// ErrFrpInvalidCommandData 表示命令数据内容异常。
	ErrFrpInvalidCommandData = errors.New("frp invalid command data")
	// ErrFrpInvalidHeartbeat 表示心跳时间格式错误。
	ErrFrpInvalidHeartbeat = errors.New("frp invalid heartbeat")
	// ErrFrpInvalidExecutedAt 表示命令回执时间格式错误。
	ErrFrpInvalidExecutedAt = errors.New("frp invalid executed_at")
)

// FrpService 承接 FRP 模块业务逻辑。
type FrpService struct {
	repo contract.FrpRepository
	rdb  *redis.Client
}

func NewFrpService(repo contract.FrpRepository, rdb *redis.Client) *FrpService {
	return &FrpService{repo: repo, rdb: rdb}
}

// UpdateDeviceInput 表示设备更新输入。
type UpdateDeviceInput struct {
	DeviceName string
	DeviceIP   string
	Status     *int8
}

// CreateMappingInput 表示映射创建输入。
type CreateMappingInput struct {
	DeviceID   int64
	LocalPort  int
	RemotePort *int
	Domain     string
	Status     *int8
}

// UpdateMappingInput 表示映射更新输入。
type UpdateMappingInput struct {
	LocalPort  *int
	RemotePort *int
	Domain     *string
	Status     *int8
}

// DeviceDetailResult 表示设备详情结果。
type DeviceDetailResult struct {
	Device           model.Device
	MappingCount     int64
	LatestConfigTime *time.Time
}

// SaveConfigInput 表示保存配置输入。
type SaveConfigInput struct {
	DeviceID   int64
	ServerAddr string
	ServerPort int
	Token      string
}

// ReportDeviceStatusInput 表示设备状态上报输入。
type ReportDeviceStatusInput struct {
	Online          bool
	LastHeartbeat   string
	LastError       string
	FailureReason   string
	ClientVersion   string
	CurrentConfigID *int64
}

// DeviceStatusResult 表示设备最近状态结果。
type DeviceStatusResult struct {
	Online          bool
	LastHeartbeat   *string
	LastError       string
	ClientVersion   string
	UpdatedAt       string
	CurrentConfigID string
}

// PullCommandResult 表示拉取到的命令。
type PullCommandResult struct {
	CommandID   string
	Action      string
	ConfigID    string
	Status      string
	CreatedAt   string
	DeliveredAt string
	Payload     string
}

// CommandResultInput 表示命令结果回传输入。
type CommandResultInput struct {
	Success       bool
	ResultMessage string
	FailureReason string
	ExecutedAt    string
}

// CreateDevice 创建设备并绑定当前用户。
func (s *FrpService) CreateDevice(ctx context.Context, uid int64, deviceName string, deviceIP string) (model.Device, error) {
	device := model.Device{
		UserID:     uid,
		DeviceName: strings.TrimSpace(deviceName),
		DeviceIP:   strings.TrimSpace(deviceIP),
		Status:     1,
	}
	return s.repo.CreateDevice(ctx, device)
}

// ListDevices 查询当前用户可见的设备；管理员可查看全部。
func (s *FrpService) ListDevices(ctx context.Context, uid int64, isAdmin bool, statusFilter *int8) ([]model.Device, error) {
	return s.repo.ListDevices(ctx, uid, isAdmin, statusFilter)
}

// GetDeviceDetail 查询设备详情（含映射数量、最近配置时间）。
func (s *FrpService) GetDeviceDetail(ctx context.Context, uid int64, isAdmin bool, deviceID int64) (DeviceDetailResult, error) {
	device, err := s.findDeviceWithPermission(ctx, uid, isAdmin, deviceID)
	if err != nil {
		return DeviceDetailResult{}, err
	}

	meta, err := s.repo.GetDeviceDetailMeta(ctx, device.ID)
	if err != nil {
		return DeviceDetailResult{}, err
	}

	return DeviceDetailResult{
		Device:           device,
		MappingCount:     meta.MappingCount,
		LatestConfigTime: meta.LatestConfigTime,
	}, nil
}

// UpdateDevice 更新设备名称、IP 和状态。
func (s *FrpService) UpdateDevice(ctx context.Context, uid int64, isAdmin bool, deviceID int64, req UpdateDeviceInput) (model.Device, error) {
	device, err := s.findDeviceWithPermission(ctx, uid, isAdmin, deviceID)
	if err != nil {
		return model.Device{}, err
	}

	updates := map[string]interface{}{}
	if strings.TrimSpace(req.DeviceName) != "" {
		updates["device_name"] = strings.TrimSpace(req.DeviceName)
	}
	if strings.TrimSpace(req.DeviceIP) != "" {
		updates["device_ip"] = strings.TrimSpace(req.DeviceIP)
	}
	if req.Status != nil {
		updates["status"] = *req.Status
	}

	if len(updates) == 0 {
		return model.Device{}, gorm.ErrInvalidData
	}

	return s.repo.UpdateDevice(ctx, device.ID, updates)
}

// DeleteDevice 删除设备及其关联映射。
func (s *FrpService) DeleteDevice(ctx context.Context, uid int64, isAdmin bool, deviceID int64) (model.Device, error) {
	device, err := s.findDeviceWithPermission(ctx, uid, isAdmin, deviceID)
	if err != nil {
		return model.Device{}, err
	}

	if err := s.repo.DeleteDevice(ctx, device.ID); err != nil {
		return model.Device{}, err
	}
	return device, nil
}

// CreateMapping 创建映射，支持自动分配 remote_port。
func (s *FrpService) CreateMapping(ctx context.Context, uid int64, isAdmin bool, req CreateMappingInput) (model.FrpMapping, error) {
	device, err := s.findDeviceWithPermission(ctx, uid, isAdmin, req.DeviceID)
	if err != nil {
		return model.FrpMapping{}, err
	}
	if device.Status == 0 {
		return model.FrpMapping{}, fmt.Errorf("device disabled")
	}

	status := int8(1)
	if req.Status != nil {
		status = *req.Status
	}

	return s.repo.CreateMapping(ctx, contract.FrpCreateMappingInput{
		DeviceID:   req.DeviceID,
		LocalPort:  req.LocalPort,
		RemotePort: req.RemotePort,
		Domain:     req.Domain,
		Status:     status,
	})
}

// ListDeviceMappings 查询某个设备下的映射列表。
func (s *FrpService) ListDeviceMappings(ctx context.Context, uid int64, isAdmin bool, deviceID int64) ([]model.FrpMapping, error) {
	_, err := s.findDeviceWithPermission(ctx, uid, isAdmin, deviceID)
	if err != nil {
		return nil, err
	}

	return s.repo.ListMappingsByDeviceID(ctx, deviceID)
}

// UpdateMapping 更新映射信息（端口、域名、状态）。
func (s *FrpService) UpdateMapping(ctx context.Context, uid int64, isAdmin bool, mappingID int64, req UpdateMappingInput) (model.FrpMapping, error) {
	mapping, device, err := s.findMappingWithPermission(ctx, uid, isAdmin, mappingID)
	if err != nil {
		return model.FrpMapping{}, err
	}
	if device.Status == 0 {
		return model.FrpMapping{}, fmt.Errorf("device disabled")
	}

	updates := map[string]interface{}{}
	if req.LocalPort != nil {
		updates["local_port"] = *req.LocalPort
	}
	if req.RemotePort != nil {
		updates["remote_port"] = *req.RemotePort
	}
	if req.Domain != nil {
		domain := strings.TrimSpace(*req.Domain)
		updates["domain"] = domain
	}
	if req.Status != nil {
		updates["status"] = *req.Status
	}
	if len(updates) == 0 {
		return model.FrpMapping{}, gorm.ErrInvalidData
	}

	return s.repo.UpdateMapping(ctx, contract.FrpUpdateMappingInput{
		MappingID:  mapping.ID,
		LocalPort:  req.LocalPort,
		RemotePort: req.RemotePort,
		Domain:     req.Domain,
		Status:     req.Status,
	})
}

// DeleteMapping 删除映射。
func (s *FrpService) DeleteMapping(ctx context.Context, uid int64, isAdmin bool, mappingID int64) (model.FrpMapping, error) {
	mapping, _, err := s.findMappingWithPermission(ctx, uid, isAdmin, mappingID)
	if err != nil {
		return model.FrpMapping{}, err
	}
	if err := s.repo.DeleteMapping(ctx, mapping.ID); err != nil {
		return model.FrpMapping{}, err
	}
	return mapping, nil
}

// PreviewConfig 预览某设备的 frpc.yaml 内容（不入库）。
func (s *FrpService) PreviewConfig(ctx context.Context, uid int64, isAdmin bool, req SaveConfigInput) (string, int64, error) {
	return s.buildConfigTextForDevice(ctx, uid, isAdmin, req.DeviceID, req.ServerAddr, req.ServerPort, req.Token)
}

// SaveConfig 生成并保存设备配置到 frp_config 表。
func (s *FrpService) SaveConfig(ctx context.Context, uid int64, isAdmin bool, req SaveConfigInput) (model.FrpConfig, error) {
	text, deviceID, err := s.buildConfigTextForDevice(ctx, uid, isAdmin, req.DeviceID, req.ServerAddr, req.ServerPort, req.Token)
	if err != nil {
		return model.FrpConfig{}, err
	}

	return s.repo.CreateConfig(ctx, model.FrpConfig{DeviceID: &deviceID, ConfigText: text})
}

// ListDeviceConfigs 查询设备配置历史（按时间倒序）。
func (s *FrpService) ListDeviceConfigs(ctx context.Context, uid int64, isAdmin bool, deviceID int64) ([]model.FrpConfig, error) {
	_, err := s.findDeviceWithPermission(ctx, uid, isAdmin, deviceID)
	if err != nil {
		return nil, err
	}

	return s.repo.ListConfigsByDeviceID(ctx, deviceID)
}

// RollbackConfig 将历史配置复制为最新版本，实现回滚。
func (s *FrpService) RollbackConfig(ctx context.Context, uid int64, isAdmin bool, configID int64) (model.FrpConfig, model.FrpConfig, error) {
	oldCfg, err := s.repo.GetConfigByID(ctx, configID)
	if err != nil {
		return model.FrpConfig{}, model.FrpConfig{}, err
	}
	if oldCfg.DeviceID == nil || *oldCfg.DeviceID <= 0 {
		return model.FrpConfig{}, model.FrpConfig{}, fmt.Errorf("config has no device")
	}

	if _, err := s.findDeviceWithPermission(ctx, uid, isAdmin, *oldCfg.DeviceID); err != nil {
		return model.FrpConfig{}, model.FrpConfig{}, err
	}

	newCfg, err := s.repo.CreateConfig(ctx, model.FrpConfig{DeviceID: oldCfg.DeviceID, ConfigText: oldCfg.ConfigText})
	if err != nil {
		return model.FrpConfig{}, model.FrpConfig{}, err
	}

	return oldCfg, newCfg, nil
}

// DeployConfig 写入配置下发命令，等待客户端执行。
func (s *FrpService) DeployConfig(ctx context.Context, uid int64, isAdmin bool, deviceID int64, requestedConfigID *int64) (string, int64, error) {
	if _, err := s.findDeviceWithPermission(ctx, uid, isAdmin, deviceID); err != nil {
		return "", 0, err
	}

	configID, err := s.resolveConfigID(ctx, deviceID, requestedConfigID)
	if err != nil {
		return "", 0, err
	}

	commandID := fmt.Sprintf("deploy-%d-%d", deviceID, time.Now().UnixNano())
	payload := map[string]interface{}{
		"command_id": commandID,
		"action":     "deploy_config",
		"device_id":  deviceID,
		"config_id":  configID,
		"created_at": time.Now().Format(time.RFC3339),
	}
	if err := s.enqueueDeviceCommand(ctx, deviceID, payload); err != nil {
		return "", 0, err
	}

	return commandID, configID, nil
}

// ReloadDevice 写入重载命令，等待客户端执行。
func (s *FrpService) ReloadDevice(ctx context.Context, uid int64, isAdmin bool, deviceID int64) (string, error) {
	if _, err := s.findDeviceWithPermission(ctx, uid, isAdmin, deviceID); err != nil {
		return "", err
	}

	commandID := fmt.Sprintf("reload-%d-%d", deviceID, time.Now().UnixNano())
	payload := map[string]interface{}{
		"command_id": commandID,
		"action":     "reload",
		"device_id":  deviceID,
		"created_at": time.Now().Format(time.RFC3339),
	}
	if err := s.enqueueDeviceCommand(ctx, deviceID, payload); err != nil {
		return "", err
	}

	return commandID, nil
}

// ReportDeviceStatus 更新设备在线状态与最近心跳。
func (s *FrpService) ReportDeviceStatus(ctx context.Context, uid int64, isAdmin bool, deviceID int64, req ReportDeviceStatusInput) error {
	if _, err := s.findDeviceWithPermission(ctx, uid, isAdmin, deviceID); err != nil {
		return err
	}

	heartbeatAt := time.Now()
	if strings.TrimSpace(req.LastHeartbeat) != "" {
		parsed, parseErr := time.Parse(time.RFC3339, strings.TrimSpace(req.LastHeartbeat))
		if parseErr != nil {
			return ErrFrpInvalidHeartbeat
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

	if err := s.rdb.HSet(ctx, statusKey, values).Err(); err != nil {
		return err
	}
	_ = s.rdb.Expire(ctx, statusKey, 7*24*time.Hour).Err()
	return nil
}

// GetDeviceStatus 查询设备最近状态。
func (s *FrpService) GetDeviceStatus(ctx context.Context, uid int64, isAdmin bool, deviceID int64) (DeviceStatusResult, error) {
	if _, err := s.findDeviceWithPermission(ctx, uid, isAdmin, deviceID); err != nil {
		return DeviceStatusResult{}, err
	}

	statusKey := frpStatusKeyPrefix + strconv.FormatInt(deviceID, 10)
	data, err := s.rdb.HGetAll(ctx, statusKey).Result()
	if err != nil {
		return DeviceStatusResult{}, err
	}
	if len(data) == 0 {
		return DeviceStatusResult{Online: false, LastHeartbeat: nil, LastError: "", ClientVersion: ""}, nil
	}

	lastHeartbeat := data["last_heartbeat"]
	return DeviceStatusResult{
		Online:          data["online"] == "1",
		LastHeartbeat:   &lastHeartbeat,
		LastError:       data["last_error"],
		ClientVersion:   data["client_version"],
		UpdatedAt:       data["updated_at"],
		CurrentConfigID: data["current_config_id"],
	}, nil
}

// PullNextCommand 拉取设备下一条待执行命令。
func (s *FrpService) PullNextCommand(ctx context.Context, uid int64, isAdmin bool, deviceID int64) (*PullCommandResult, error) {
	if _, err := s.findDeviceWithPermission(ctx, uid, isAdmin, deviceID); err != nil {
		return nil, err
	}

	queueKey := frpCommandQueuePrefix + strconv.FormatInt(deviceID, 10)
	rawToken, err := s.rdb.LPop(ctx, queueKey).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, nil
		}
		return nil, err
	}

	commandID := strings.TrimSpace(rawToken)
	if strings.HasPrefix(commandID, "{") {
		legacyID, convertErr := s.convertLegacyQueuedCommand(ctx, deviceID, commandID)
		if convertErr != nil {
			return nil, ErrFrpInvalidQueuedCommand
		}
		commandID = legacyID
	}

	dataKey := frpCommandDataKeyPrefix + commandID
	cmdData, err := s.rdb.HGetAll(ctx, dataKey).Result()
	if err != nil {
		return nil, err
	}
	if len(cmdData) == 0 {
		return nil, ErrFrpCommandDataNotFound
	}

	now := time.Now().Format(time.RFC3339)
	_ = s.rdb.HSet(ctx, dataKey, map[string]interface{}{
		"status":       "delivered",
		"delivered_at": now,
		"updated_at":   now,
	}).Err()

	return &PullCommandResult{
		CommandID:   commandID,
		Action:      cmdData["action"],
		ConfigID:    cmdData["config_id"],
		Status:      "delivered",
		CreatedAt:   cmdData["created_at"],
		DeliveredAt: now,
		Payload:     cmdData["payload"],
	}, nil
}

// ReportCommandResult 更新命令执行结果。
func (s *FrpService) ReportCommandResult(ctx context.Context, uid int64, isAdmin bool, commandID string, req CommandResultInput) (int64, string, error) {
	dataKey := frpCommandDataKeyPrefix + commandID
	cmdData, err := s.rdb.HGetAll(ctx, dataKey).Result()
	if err != nil {
		return 0, "", err
	}
	if len(cmdData) == 0 {
		return 0, "", ErrFrpCommandNotFound
	}

	deviceID, parseErr := strconv.ParseInt(cmdData["device_id"], 10, 64)
	if parseErr != nil || deviceID <= 0 {
		return 0, "", ErrFrpInvalidCommandData
	}

	if _, err := s.findDeviceWithPermission(ctx, uid, isAdmin, deviceID); err != nil {
		return 0, "", err
	}

	executedAt := time.Now()
	if strings.TrimSpace(req.ExecutedAt) != "" {
		parsed, pErr := time.Parse(time.RFC3339, strings.TrimSpace(req.ExecutedAt))
		if pErr != nil {
			return 0, "", ErrFrpInvalidExecutedAt
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

	if err := s.rdb.HSet(ctx, dataKey, map[string]interface{}{
		"status":         status,
		"result_message": msg,
		"failure_reason": failureReason,
		"executed_at":    executedAt.Format(time.RFC3339),
		"updated_at":     time.Now().Format(time.RFC3339),
	}).Err(); err != nil {
		return 0, "", err
	}

	return deviceID, status, nil
}

// ListDeviceCommands 查询设备命令历史。
func (s *FrpService) ListDeviceCommands(ctx context.Context, uid int64, isAdmin bool, deviceID int64, limit int) ([]map[string]string, error) {
	if _, err := s.findDeviceWithPermission(ctx, uid, isAdmin, deviceID); err != nil {
		return nil, err
	}

	historyKey := frpCommandHistoryPrefix + strconv.FormatInt(deviceID, 10)
	ids, err := s.rdb.LRange(ctx, historyKey, 0, int64(limit-1)).Result()
	if err != nil {
		return nil, err
	}

	items := make([]map[string]string, 0, len(ids))
	for _, commandID := range ids {
		dataKey := frpCommandDataKeyPrefix + strings.TrimSpace(commandID)
		row, hErr := s.rdb.HGetAll(ctx, dataKey).Result()
		if hErr != nil || len(row) == 0 {
			continue
		}
		items = append(items, map[string]string{
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

	return items, nil
}

// LogOperation 写入 FRP 关键操作日志。
func (s *FrpService) LogOperation(ctx context.Context, userID int64, action string, detail string, clientIP string) error {
	if len(detail) > 500 {
		detail = detail[:500]
	}
	uid := userID
	return s.repo.CreateOperationLog(ctx, model.OperationLog{
		UserID:   &uid,
		Action:   action,
		Status:   "success",
		Detail:   detail,
		ClientIP: clientIP,
	})
}

// findDeviceWithPermission 查询设备并校验当前用户是否有权限访问。
func (s *FrpService) findDeviceWithPermission(ctx context.Context, userID int64, isAdmin bool, deviceID int64) (model.Device, error) {
	device, err := s.repo.GetDeviceByID(ctx, deviceID)
	if err != nil {
		return model.Device{}, err
	}
	if isAdmin || device.UserID == userID {
		return device, nil
	}
	return model.Device{}, ErrFrpForbidden
}

// findMappingWithPermission 查询映射并校验当前用户权限。
func (s *FrpService) findMappingWithPermission(ctx context.Context, userID int64, isAdmin bool, mappingID int64) (model.FrpMapping, model.Device, error) {
	mapping, err := s.repo.GetMappingByID(ctx, mappingID)
	if err != nil {
		return model.FrpMapping{}, model.Device{}, err
	}
	device, err := s.findDeviceWithPermission(ctx, userID, isAdmin, mapping.DeviceID)
	if err != nil {
		return model.FrpMapping{}, model.Device{}, err
	}
	return mapping, device, nil
}

// buildConfigTextForDevice 根据设备和映射生成 frpc.yaml 文本。
func (s *FrpService) buildConfigTextForDevice(ctx context.Context, userID int64, isAdmin bool, deviceID int64, serverAddr string, serverPort int, token string) (string, int64, error) {
	if deviceID <= 0 {
		return "", 0, ErrFrpBadConfig
	}

	if _, err := s.findDeviceWithPermission(ctx, userID, isAdmin, deviceID); err != nil {
		return "", 0, err
	}

	serverAddr = strings.TrimSpace(serverAddr)
	if serverAddr == "" {
		return "", 0, ErrFrpNeedServer
	}
	if serverPort <= 0 {
		serverPort = 7000
	}
	if serverPort > 65535 {
		return "", 0, ErrFrpBadConfig
	}

	mappings, err := s.repo.ListActiveMappingsByDeviceID(ctx, deviceID)
	if err != nil {
		return "", 0, err
	}
	if len(mappings) == 0 {
		return "", 0, ErrFrpNoMapping
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
	b.WriteString(fmt.Sprintf("%d", serverPort))
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
		b.WriteString(fmt.Sprintf("%d", m.LocalPort))
		b.WriteString("\n")

		if typeName == "tcp" {
			b.WriteString("    remotePort: ")
			b.WriteString(fmt.Sprintf("%d", m.RemotePort))
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

// resolveConfigID 解析要下发的配置 ID；如果请求未指定则选择设备最新配置。
func (s *FrpService) resolveConfigID(ctx context.Context, deviceID int64, requested *int64) (int64, error) {
	configID, err := s.repo.ResolveConfigID(ctx, deviceID, requested)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) && (requested == nil || *requested <= 0) {
			return 0, ErrFrpNoConfigForDevice
		}
		if strings.Contains(strings.ToLower(err.Error()), "frp no config for device") {
			return 0, ErrFrpNoConfigForDevice
		}
		return 0, err
	}
	return configID, nil
}

// enqueueDeviceCommand 将命令写入设备队列，供客户端轮询拉取后执行。
func (s *FrpService) enqueueDeviceCommand(ctx context.Context, deviceID int64, payload map[string]interface{}) error {
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
	if err := s.persistCommandData(ctx, commandID, deviceID, action, configID, "pending", string(raw), now); err != nil {
		return err
	}
	if err := s.rdb.RPush(ctx, queueKey, commandID).Err(); err != nil {
		return err
	}
	if err := s.rdb.Expire(ctx, queueKey, 7*24*time.Hour).Err(); err != nil {
		return err
	}
	if err := s.rdb.LPush(ctx, historyKey, commandID).Err(); err != nil {
		return err
	}
	if err := s.rdb.LTrim(ctx, historyKey, 0, frpCommandHistoryLimit-1).Err(); err != nil {
		return err
	}
	if err := s.rdb.Expire(ctx, historyKey, 7*24*time.Hour).Err(); err != nil {
		return err
	}
	if err := s.rdb.Set(ctx, lastKey, string(raw), 7*24*time.Hour).Err(); err != nil {
		return err
	}
	return nil
}

// convertLegacyQueuedCommand 兼容旧队列格式（整段 JSON），并转换为标准命令数据。
func (s *FrpService) convertLegacyQueuedCommand(ctx context.Context, deviceID int64, raw string) (string, error) {
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
	if err := s.persistCommandData(ctx, commandID, deviceID, action, configID, "pending", raw, now); err != nil {
		return "", err
	}
	historyKey := frpCommandHistoryPrefix + strconv.FormatInt(deviceID, 10)
	if err := s.rdb.LPush(ctx, historyKey, commandID).Err(); err != nil {
		return "", err
	}
	if err := s.rdb.LTrim(ctx, historyKey, 0, frpCommandHistoryLimit-1).Err(); err != nil {
		return "", err
	}
	if err := s.rdb.Expire(ctx, historyKey, 7*24*time.Hour).Err(); err != nil {
		return "", err
	}
	return commandID, nil
}

// persistCommandData 持久化命令基础信息，用于后续状态流转和历史查询。
func (s *FrpService) persistCommandData(ctx context.Context, commandID string, deviceID int64, action string, configID int64, status string, payload string, now string) error {
	commandDataKey := frpCommandDataKeyPrefix + commandID
	if err := s.rdb.HSet(ctx, commandDataKey, map[string]interface{}{
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
	if err := s.rdb.Expire(ctx, commandDataKey, 7*24*time.Hour).Err(); err != nil {
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

// isDuplicateKeyErr 判断是否为唯一键冲突。
func isDuplicateKeyErr(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "duplicate") || strings.Contains(text, "1062")
}
