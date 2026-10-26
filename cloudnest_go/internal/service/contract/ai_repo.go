package contract

import (
	"context"
	"time"

	"cloudenest_go/model"
)

// AISearchQuery 定义 AI 检索仓储查询参数。
type AISearchQuery struct {
	Keyword   string
	FileType  string
	Location  string
	Landmark  string
	StartTime *time.Time
	EndTime   *time.Time
	TagOnly   bool
	Page      int
	Limit     int
}

// AISearchRow 定义 AI 检索返回行。
type AISearchRow struct {
	ID          int64
	Filename    string
	FileType    string
	Size        int64
	ParentID    int64
	UserID      int64
	CreateTime  time.Time
	UpdateTime  time.Time
	Tags        string
	Description string
	OCRText     string
}

// AIRepository 定义 AI 模块存储访问能力。
type AIRepository interface {
	ScanHybrid(ctx context.Context, uid int64, filter AIHybridFilter, visit func(AIHybridRow) error) error
	ValidateHybrid(ctx context.Context, uid int64, rows []AIHybridRow) (map[int64]bool, error)
	ApplySettingWithAudit(ctx context.Context, uid int64, key string, value string, description string, clientIP string) error
	Search(ctx context.Context, uid int64, q AISearchQuery) ([]AISearchRow, int64, error)
	GetActiveFileByID(ctx context.Context, fileID int64) (model.File, error)
	GetStorageByID(ctx context.Context, storageID int64) (model.Storage, error)
	CountFilePermissionsByLevels(ctx context.Context, fileID int64, userID int64, permissions []string) (int64, error)
	GetAIMetadataByFileID(ctx context.Context, fileID int64) (model.AiMetadata, error)
	CreateAIMetadata(ctx context.Context, row model.AiMetadata) (model.AiMetadata, error)
	UpdateAIMetadata(ctx context.Context, row model.AiMetadata, tags string, desc string, ocrText string) (model.AiMetadata, error)
	CreateAITask(ctx context.Context, row model.AiTask) error
}

type AIHybridFilter struct {
	FileTypes      []string
	UploadedAfter  *time.Time
	UploadedBefore *time.Time
}

// Rows are streamed in file_id/ordinal order; only live authorized files and
// chunks from the matching completed source generation may be exposed.
type AIHybridRow struct {
	ID             int64
	Filename       string
	FileType       string
	Size           int64
	ParentID       int64
	UserID         int64
	CreateTime     time.Time
	UpdateTime     time.Time
	SourceHash     string
	Generation     int64
	EmbeddingModel string
	ModelDigest    string
	Dimensions     int
	Ordinal        int
	StartRune      int
	Content        string
	Vector         []byte
}
