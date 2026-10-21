package model

import (
	"time"
)

// User 用户表
type User struct {
	AuthVersion uint64    `gorm:"not null;default:0" json:"-"`
	ID          int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	Username    string    `gorm:"type:varchar(50);not null;unique" json:"username"`
	Password    string    `gorm:"type:varchar(255);not null" json:"-"`
	Email       string    `gorm:"type:varchar(100)" json:"email"`
	Status      int8      `gorm:"type:tinyint;default:1;comment:1正常 0禁用" json:"status"`
	CreateTime  time.Time `gorm:"type:datetime;default:CURRENT_TIMESTAMP" json:"create_time"`
	UpdateTime  time.Time `gorm:"type:datetime;default:CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP" json:"update_time"`
}

func (User) TableName() string {
	return "user"
}

// Role 角色表
type Role struct {
	ID          int64  `gorm:"primaryKey;autoIncrement" json:"id"`
	RoleName    string `gorm:"type:varchar(50);not null;unique" json:"role_name"`
	Description string `gorm:"type:varchar(255)" json:"description"`
}

func (Role) TableName() string {
	return "role"
}

// UserRole 用户角色关联表
type UserRole struct {
	ID     int64 `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID int64 `gorm:"not null;index" json:"user_id"`
	RoleID int64 `gorm:"not null;index" json:"role_id"`
}

func (UserRole) TableName() string {
	return "user_role"
}

// Storage 存储表
type Storage struct {
	ID         int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	FileHash   string    `gorm:"type:char(64);not null;unique" json:"file_hash"`
	FilePath   string    `gorm:"type:varchar(500);not null" json:"file_path"`
	FileSize   *int64    `gorm:"type:bigint" json:"file_size"`
	CreateTime time.Time `gorm:"type:datetime;default:CURRENT_TIMESTAMP" json:"create_time"`
}

func (Storage) TableName() string {
	return "storage"
}

// Workspace 工作区
type Workspace struct {
	ID         int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	Name       string    `gorm:"type:varchar(100);not null" json:"name"`
	OwnerID    int64     `gorm:"not null;index" json:"owner_id"`
	CreateTime time.Time `gorm:"type:datetime;default:CURRENT_TIMESTAMP" json:"create_time"`
}

func (Workspace) TableName() string {
	return "workspace"
}

// File 文件表
type File struct {
	ID             int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID         int64     `gorm:"not null;index" json:"user_id"`
	Filename       string    `gorm:"type:varchar(255);not null" json:"filename"`
	ParentID       int64     `gorm:"default:0;index" json:"parent_id"`
	IsFolder       int8      `gorm:"type:tinyint;default:0" json:"is_folder"`
	FileType       string    `gorm:"type:varchar(50)" json:"file_type"`
	Size           int64     `gorm:"default:0" json:"size"`
	StorageID      *int64    `gorm:"type:bigint" json:"storage_id"`
	WorkspaceID    *int64    `gorm:"type:bigint;index" json:"workspace_id"`
	CurrentVersion int       `gorm:"default:1" json:"current_version"`
	IsDeleted      int8      `gorm:"type:tinyint;default:0" json:"is_deleted"`
	CreateTime     time.Time `gorm:"type:datetime;default:CURRENT_TIMESTAMP" json:"create_time"`
	UpdateTime     time.Time `gorm:"type:datetime;default:CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP" json:"update_time"`
}

func (File) TableName() string {
	return "file"
}

// UploadChunk 分片上传块
type UploadChunk struct {
	ID         int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	UploadID   string    `gorm:"type:varchar(100);not null;uniqueIndex:idx_upload_chunk" json:"upload_id"`
	ChunkIndex int       `gorm:"not null;uniqueIndex:idx_upload_chunk" json:"chunk_index"`
	ChunkSize  *int64    `gorm:"type:bigint" json:"chunk_size"`
	ChunkPath  string    `gorm:"type:varchar(500)" json:"chunk_path"`
	Status     int8      `gorm:"type:tinyint;default:0;comment:0未完成 1已完成" json:"status"`
	CreateTime time.Time `gorm:"type:datetime;default:CURRENT_TIMESTAMP" json:"create_time"`
}

func (UploadChunk) TableName() string {
	return "upload_chunk"
}

// UploadTask 分片上传任务
type UploadTask struct {
	ID             int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	UploadID       string    `gorm:"type:varchar(100);not null;unique" json:"upload_id"`
	UserID         *int64    `gorm:"type:bigint" json:"user_id"`
	Filename       string    `gorm:"type:varchar(255)" json:"filename"`
	TotalSize      *int64    `gorm:"type:bigint" json:"total_size"`
	ChunkSize      *int      `gorm:"type:int" json:"chunk_size"`
	TotalChunks    *int      `gorm:"type:int" json:"total_chunks"`
	UploadedChunks int       `gorm:"type:int;default:0" json:"uploaded_chunks"`
	Status         int8      `gorm:"type:tinyint;default:0;comment:0上传中 1完成" json:"status"`
	CreateTime     time.Time `gorm:"type:datetime;default:CURRENT_TIMESTAMP" json:"create_time"`
}

func (UploadTask) TableName() string {
	return "upload_task"
}

// UserPathPermission 用户路径权限
type UserPathPermission struct {
	ID         int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID     int64     `gorm:"not null;index;uniqueIndex:idx_user_path_method" json:"user_id"`
	Path       string    `gorm:"type:varchar(255);not null;uniqueIndex:idx_user_path_method" json:"path"`
	Method     string    `gorm:"type:varchar(10);not null;default:*;uniqueIndex:idx_user_path_method" json:"method"`
	CreateTime time.Time `gorm:"type:datetime;default:CURRENT_TIMESTAMP" json:"create_time"`
}

func (UserPathPermission) TableName() string {
	return "user_path_permission"
}

// FileVersion 文件版本
type FileVersion struct {
	ID         int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	FileID     int64     `gorm:"not null;uniqueIndex:idx_file_version" json:"file_id"` // Combined unique index simulation
	Version    int       `gorm:"not null;uniqueIndex:idx_file_version" json:"version"`
	StorageID  int64     `gorm:"not null" json:"storage_id"`
	Size       *int64    `gorm:"type:bigint" json:"size"`
	CreateUser *int64    `gorm:"type:bigint" json:"create_user"`
	Comment    string    `gorm:"type:varchar(255)" json:"comment"`
	CreateTime time.Time `gorm:"type:datetime;default:CURRENT_TIMESTAMP" json:"create_time"`
}

func (FileVersion) TableName() string {
	return "file_version"
}

// FilePermission 文件权限
type FilePermission struct {
	ID         int64  `gorm:"primaryKey;autoIncrement" json:"id"`
	FileID     int64  `gorm:"not null;index" json:"file_id"`
	UserID     int64  `gorm:"not null;index" json:"user_id"`
	Permission string `gorm:"type:enum('read','write','download');not null" json:"permission"`
}

func (FilePermission) TableName() string {
	return "file_permission"
}

// LivePhoto Live Photo
type LivePhoto struct {
	ID          int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	ImageFileID int64     `gorm:"not null" json:"image_file_id"`
	VideoFileID int64     `gorm:"not null" json:"video_file_id"`
	CreateTime  time.Time `gorm:"type:datetime;default:CURRENT_TIMESTAMP" json:"create_time"`
}

func (LivePhoto) TableName() string {
	return "live_photo"
}

// WorkspaceUser 协作空间用户
type WorkspaceUser struct {
	ID          int64  `gorm:"primaryKey;autoIncrement" json:"id"`
	WorkspaceID int64  `gorm:"not null;uniqueIndex:idx_ws_user" json:"workspace_id"`
	UserID      int64  `gorm:"not null;uniqueIndex:idx_ws_user" json:"user_id"`
	Role        string `gorm:"type:varchar(20);not null" json:"role"`
}

func (WorkspaceUser) TableName() string {
	return "workspace_user"
}

// Device 设备表
type Device struct {
	ID         int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID     int64     `gorm:"not null;index" json:"user_id"`
	DeviceName string    `gorm:"type:varchar(100)" json:"device_name"`
	DeviceIP   string    `gorm:"type:varchar(50);column:device_ip" json:"device_ip"`
	Status     int8      `gorm:"type:tinyint;default:1" json:"status"`
	CreateTime time.Time `gorm:"type:datetime;default:CURRENT_TIMESTAMP" json:"create_time"`
}

func (Device) TableName() string {
	return "device"
}

// FrpMapping FRP映射
type FrpMapping struct {
	ID         int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	DeviceID   int64     `gorm:"not null" json:"device_id"`
	LocalPort  int       `gorm:"not null" json:"local_port"`
	RemotePort int       `gorm:"not null;unique" json:"remote_port"`
	Domain     string    `gorm:"type:varchar(100)" json:"domain"`
	Status     int8      `gorm:"type:tinyint;default:1" json:"status"`
	CreateTime time.Time `gorm:"type:datetime;default:CURRENT_TIMESTAMP" json:"create_time"`
}

func (FrpMapping) TableName() string {
	return "frp_mapping"
}

// FrpConfig FRP配置
type FrpConfig struct {
	ID         int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	DeviceID   *int64    `gorm:"type:bigint" json:"device_id"`
	ConfigText string    `gorm:"type:text" json:"config_text"`
	CreateTime time.Time `gorm:"type:datetime;default:CURRENT_TIMESTAMP" json:"create_time"`
}

func (FrpConfig) TableName() string {
	return "frp_config"
}

// AiMetadata AI元数据
type AiMetadata struct {
	ID          int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	FileID      int64     `gorm:"not null" json:"file_id"`
	Tags        string    `gorm:"type:varchar(255)" json:"tags"`
	Description string    `gorm:"type:text" json:"description"`
	OcrText     string    `gorm:"type:text" json:"ocr_text"`
	CreateTime  time.Time `gorm:"type:datetime;default:CURRENT_TIMESTAMP" json:"create_time"`
}

func (AiMetadata) TableName() string {
	return "ai_metadata"
}

// AiTask AI任务
type AiTask struct {
	ID         int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID     *int64    `gorm:"type:bigint;index" json:"user_id"`
	FileID     *int64    `gorm:"type:bigint;index" json:"file_id"`
	TaskType   string    `gorm:"type:varchar(50)" json:"task_type"`
	Status     string    `gorm:"type:varchar(20)" json:"status"`
	Result     string    `gorm:"type:text" json:"result"`
	CreateTime time.Time `gorm:"type:datetime;default:CURRENT_TIMESTAMP" json:"create_time"`
}

func (AiTask) TableName() string {
	return "ai_task"
}

// Share 分享
type Share struct {
	ID         int64      `gorm:"primaryKey;autoIncrement" json:"id"`
	FileID     *int64     `gorm:"type:bigint" json:"file_id"`
	UserID     *int64     `gorm:"type:bigint" json:"user_id"`
	ShareCode  string     `gorm:"type:varchar(20);unique" json:"share_code"`
	ExpireTime *time.Time `gorm:"type:datetime" json:"expire_time"`
	CreateTime time.Time  `gorm:"type:datetime;default:CURRENT_TIMESTAMP" json:"create_time"`
}

func (Share) TableName() string {
	return "share"
}

// OperationLog 操作日志
type OperationLog struct {
	ID         int64     `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID     *int64    `gorm:"type:bigint;index" json:"user_id"`
	FileID     *int64    `gorm:"type:bigint;index" json:"file_id"`
	Action     string    `gorm:"type:varchar(50);not null;index" json:"action"`
	Status     string    `gorm:"type:varchar(20);not null;default:success" json:"status"`
	Detail     string    `gorm:"type:varchar(500)" json:"detail"`
	ClientIP   string    `gorm:"type:varchar(64)" json:"client_ip"`
	CreateTime time.Time `gorm:"type:datetime;default:CURRENT_TIMESTAMP" json:"create_time"`
}

func (OperationLog) TableName() string {
	return "operation_log"
}

// SystemSetting 系统设置
type SystemSetting struct {
	ID           int64  `gorm:"primaryKey;autoIncrement" json:"id"`
	SettingKey   string `gorm:"type:varchar(100);unique" json:"setting_key"`
	SettingValue string `gorm:"type:varchar(255)" json:"setting_value"`
	Description  string `gorm:"type:varchar(255)" json:"description"`
}

func (SystemSetting) TableName() string {
	return "system_setting"
}
