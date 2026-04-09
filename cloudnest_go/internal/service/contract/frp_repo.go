package contract

import (
	"context"
	"time"

	"cloudenest_go/model"
)

// FrpCreateMappingInput 定义创建映射输入。
type FrpCreateMappingInput struct {
	DeviceID   int64
	LocalPort  int
	RemotePort *int
	Domain     string
	Status     int8
}

// FrpUpdateMappingInput 定义更新映射输入。
type FrpUpdateMappingInput struct {
	MappingID  int64
	LocalPort  *int
	RemotePort *int
	Domain     *string
	Status     *int8
}

// FrpDeviceDetailMeta 定义设备详情附加信息。
type FrpDeviceDetailMeta struct {
	MappingCount     int64
	LatestConfigTime *time.Time
}

// FrpRepository 定义 FRP 模块存储访问能力。
type FrpRepository interface {
	CreateDevice(ctx context.Context, device model.Device) (model.Device, error)
	ListDevices(ctx context.Context, uid int64, isAdmin bool, statusFilter *int8) ([]model.Device, error)
	GetDeviceByID(ctx context.Context, deviceID int64) (model.Device, error)
	GetDeviceDetailMeta(ctx context.Context, deviceID int64) (FrpDeviceDetailMeta, error)
	UpdateDevice(ctx context.Context, deviceID int64, updates map[string]interface{}) (model.Device, error)
	DeleteDevice(ctx context.Context, deviceID int64) error
	CreateMapping(ctx context.Context, req FrpCreateMappingInput) (model.FrpMapping, error)
	ListMappingsByDeviceID(ctx context.Context, deviceID int64) ([]model.FrpMapping, error)
	GetMappingByID(ctx context.Context, mappingID int64) (model.FrpMapping, error)
	UpdateMapping(ctx context.Context, req FrpUpdateMappingInput) (model.FrpMapping, error)
	DeleteMapping(ctx context.Context, mappingID int64) error
	ListActiveMappingsByDeviceID(ctx context.Context, deviceID int64) ([]model.FrpMapping, error)
	CreateConfig(ctx context.Context, cfg model.FrpConfig) (model.FrpConfig, error)
	ListConfigsByDeviceID(ctx context.Context, deviceID int64) ([]model.FrpConfig, error)
	GetConfigByID(ctx context.Context, configID int64) (model.FrpConfig, error)
	ResolveConfigID(ctx context.Context, deviceID int64, requested *int64) (int64, error)
	CreateOperationLog(ctx context.Context, row model.OperationLog) error
}
