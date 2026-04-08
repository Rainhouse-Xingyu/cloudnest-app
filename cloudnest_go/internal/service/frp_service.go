package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"cloudenest_go/model"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

const (
	// frp 默认远程端口池范围。
	defaultFrpRemotePortStart = 20000
	defaultFrpRemotePortEnd   = 20999
	maxAllocateRetries        = 5
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
)

// FrpService 承接 FRP 模块业务逻辑。
type FrpService struct {
	db  *gorm.DB
	rdb *redis.Client
}

func NewFrpService(db *gorm.DB, rdb *redis.Client) *FrpService {
	return &FrpService{db: db, rdb: rdb}
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

// CreateDevice 创建设备并绑定当前用户。
func (s *FrpService) CreateDevice(ctx context.Context, uid int64, deviceName string, deviceIP string) (model.Device, error) {
	device := model.Device{
		UserID:     uid,
		DeviceName: strings.TrimSpace(deviceName),
		DeviceIP:   strings.TrimSpace(deviceIP),
		Status:     1,
	}
	if err := s.db.WithContext(ctx).Create(&device).Error; err != nil {
		return model.Device{}, err
	}
	return device, nil
}

// ListDevices 查询当前用户可见的设备；管理员可查看全部。
func (s *FrpService) ListDevices(ctx context.Context, uid int64, isAdmin bool, statusFilter *int8) ([]model.Device, error) {
	query := s.db.WithContext(ctx).Model(&model.Device{})
	if !isAdmin {
		query = query.Where("user_id = ?", uid)
	}
	if statusFilter != nil {
		query = query.Where("status = ?", *statusFilter)
	}

	var rows []model.Device
	if err := query.Order("id DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// GetDeviceDetail 查询设备详情（含映射数量、最近配置时间）。
func (s *FrpService) GetDeviceDetail(ctx context.Context, uid int64, isAdmin bool, deviceID int64) (DeviceDetailResult, error) {
	device, err := s.findDeviceWithPermission(ctx, uid, isAdmin, deviceID)
	if err != nil {
		return DeviceDetailResult{}, err
	}

	var mappingCount int64
	if err := s.db.WithContext(ctx).Model(&model.FrpMapping{}).Where("device_id = ?", device.ID).Count(&mappingCount).Error; err != nil {
		return DeviceDetailResult{}, err
	}

	var latestCfg model.FrpConfig
	var latestConfigTime *time.Time
	if err := s.db.WithContext(ctx).Where("device_id = ?", device.ID).Order("id DESC").First(&latestCfg).Error; err == nil {
		latestConfigTime = &latestCfg.CreateTime
	}

	return DeviceDetailResult{
		Device:           device,
		MappingCount:     mappingCount,
		LatestConfigTime: latestConfigTime,
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

	if err := s.db.WithContext(ctx).Model(&model.Device{}).Where("id = ?", device.ID).Updates(updates).Error; err != nil {
		return model.Device{}, err
	}
	if err := s.db.WithContext(ctx).First(&device, device.ID).Error; err != nil {
		return model.Device{}, err
	}

	return device, nil
}

// DeleteDevice 删除设备及其关联映射。
func (s *FrpService) DeleteDevice(ctx context.Context, uid int64, isAdmin bool, deviceID int64) (model.Device, error) {
	device, err := s.findDeviceWithPermission(ctx, uid, isAdmin, deviceID)
	if err != nil {
		return model.Device{}, err
	}

	if err := s.db.WithContext(ctx).Delete(&model.Device{}, "id = ?", device.ID).Error; err != nil {
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

	var created model.FrpMapping
	allocErr := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if req.Domain != "" {
			var conflictCount int64
			if err := tx.Model(&model.FrpMapping{}).Where("domain = ?", req.Domain).Count(&conflictCount).Error; err != nil {
				return err
			}
			if conflictCount > 0 {
				return fmt.Errorf("domain already exists")
			}
		}

		remotePort := 0
		if req.RemotePort != nil {
			remotePort = *req.RemotePort
		} else {
			p, err := s.allocateRemotePort(tx)
			if err != nil {
				return err
			}
			remotePort = p
		}

		created = model.FrpMapping{
			DeviceID:   req.DeviceID,
			LocalPort:  req.LocalPort,
			RemotePort: remotePort,
			Domain:     req.Domain,
			Status:     status,
		}
		if err := tx.Create(&created).Error; err != nil {
			if isDuplicateKeyErr(err) {
				return fmt.Errorf("remote_port already exists")
			}
			return err
		}
		return nil
	})
	if allocErr != nil {
		return model.FrpMapping{}, allocErr
	}
	return created, nil
}

// ListDeviceMappings 查询某个设备下的映射列表。
func (s *FrpService) ListDeviceMappings(ctx context.Context, uid int64, isAdmin bool, deviceID int64) ([]model.FrpMapping, error) {
	_, err := s.findDeviceWithPermission(ctx, uid, isAdmin, deviceID)
	if err != nil {
		return nil, err
	}

	var rows []model.FrpMapping
	if err := s.db.WithContext(ctx).Where("device_id = ?", deviceID).Order("id DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
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

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if domainAny, ok := updates["domain"]; ok {
			domain := domainAny.(string)
			if domain != "" {
				var conflict int64
				if err := tx.Model(&model.FrpMapping{}).Where("domain = ? AND id <> ?", domain, mapping.ID).Count(&conflict).Error; err != nil {
					return err
				}
				if conflict > 0 {
					return fmt.Errorf("domain already exists")
				}
			}
		}

		if err := tx.Model(&model.FrpMapping{}).Where("id = ?", mapping.ID).Updates(updates).Error; err != nil {
			if isDuplicateKeyErr(err) {
				return fmt.Errorf("remote_port already exists")
			}
			return err
		}
		return nil
	})
	if err != nil {
		return model.FrpMapping{}, err
	}

	if err := s.db.WithContext(ctx).First(&mapping, mapping.ID).Error; err != nil {
		return model.FrpMapping{}, err
	}
	return mapping, nil
}

// DeleteMapping 删除映射。
func (s *FrpService) DeleteMapping(ctx context.Context, uid int64, isAdmin bool, mappingID int64) (model.FrpMapping, error) {
	mapping, _, err := s.findMappingWithPermission(ctx, uid, isAdmin, mappingID)
	if err != nil {
		return model.FrpMapping{}, err
	}
	if err := s.db.WithContext(ctx).Delete(&model.FrpMapping{}, "id = ?", mapping.ID).Error; err != nil {
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

	cfg := model.FrpConfig{DeviceID: &deviceID, ConfigText: text}
	if err := s.db.WithContext(ctx).Create(&cfg).Error; err != nil {
		return model.FrpConfig{}, err
	}
	return cfg, nil
}

// ListDeviceConfigs 查询设备配置历史（按时间倒序）。
func (s *FrpService) ListDeviceConfigs(ctx context.Context, uid int64, isAdmin bool, deviceID int64) ([]model.FrpConfig, error) {
	_, err := s.findDeviceWithPermission(ctx, uid, isAdmin, deviceID)
	if err != nil {
		return nil, err
	}

	var rows []model.FrpConfig
	if err := s.db.WithContext(ctx).Where("device_id = ?", deviceID).Order("id DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// RollbackConfig 将历史配置复制为最新版本，实现回滚。
func (s *FrpService) RollbackConfig(ctx context.Context, uid int64, isAdmin bool, configID int64) (model.FrpConfig, model.FrpConfig, error) {
	var oldCfg model.FrpConfig
	if err := s.db.WithContext(ctx).First(&oldCfg, "id = ?", configID).Error; err != nil {
		return model.FrpConfig{}, model.FrpConfig{}, err
	}
	if oldCfg.DeviceID == nil || *oldCfg.DeviceID <= 0 {
		return model.FrpConfig{}, model.FrpConfig{}, fmt.Errorf("config has no device")
	}

	if _, err := s.findDeviceWithPermission(ctx, uid, isAdmin, *oldCfg.DeviceID); err != nil {
		return model.FrpConfig{}, model.FrpConfig{}, err
	}

	newCfg := model.FrpConfig{DeviceID: oldCfg.DeviceID, ConfigText: oldCfg.ConfigText}
	if err := s.db.WithContext(ctx).Create(&newCfg).Error; err != nil {
		return model.FrpConfig{}, model.FrpConfig{}, err
	}

	return oldCfg, newCfg, nil
}

// LogOperation 写入 FRP 关键操作日志。
func (s *FrpService) LogOperation(ctx context.Context, userID int64, action string, detail string, clientIP string) error {
	if len(detail) > 500 {
		detail = detail[:500]
	}
	uid := userID
	return s.db.WithContext(ctx).Create(&model.OperationLog{
		UserID:   &uid,
		Action:   action,
		Status:   "success",
		Detail:   detail,
		ClientIP: clientIP,
	}).Error
}

// findDeviceWithPermission 查询设备并校验当前用户是否有权限访问。
func (s *FrpService) findDeviceWithPermission(ctx context.Context, userID int64, isAdmin bool, deviceID int64) (model.Device, error) {
	var device model.Device
	if err := s.db.WithContext(ctx).First(&device, "id = ?", deviceID).Error; err != nil {
		return model.Device{}, err
	}
	if isAdmin || device.UserID == userID {
		return device, nil
	}
	return model.Device{}, ErrFrpForbidden
}

// findMappingWithPermission 查询映射并校验当前用户权限。
func (s *FrpService) findMappingWithPermission(ctx context.Context, userID int64, isAdmin bool, mappingID int64) (model.FrpMapping, model.Device, error) {
	var mapping model.FrpMapping
	if err := s.db.WithContext(ctx).First(&mapping, "id = ?", mappingID).Error; err != nil {
		return model.FrpMapping{}, model.Device{}, err
	}
	device, err := s.findDeviceWithPermission(ctx, userID, isAdmin, mapping.DeviceID)
	if err != nil {
		return model.FrpMapping{}, model.Device{}, err
	}
	return mapping, device, nil
}

// allocateRemotePort 在固定端口池中分配可用端口。
func (s *FrpService) allocateRemotePort(tx *gorm.DB) (int, error) {
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

	var mappings []model.FrpMapping
	if err := s.db.WithContext(ctx).Where("device_id = ? AND status = 1", deviceID).Order("id ASC").Find(&mappings).Error; err != nil {
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

// isDuplicateKeyErr 判断是否为唯一键冲突。
func isDuplicateKeyErr(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "duplicate") || strings.Contains(text, "1062")
}
