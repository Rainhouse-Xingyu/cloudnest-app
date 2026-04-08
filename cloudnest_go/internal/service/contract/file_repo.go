package contract

import (
	"context"

	"cloudenest_go/model"
)

// FileSearchQuery 定义文件检索参数。
type FileSearchQuery struct {
	ParentID  int64
	Name      string
	FileType  string
	StartTime string
	EndTime   string
	Page      int
	Limit     int
}

// BatchDownloadEntry 定义批量下载可用条目。
type BatchDownloadEntry struct {
	File        model.File
	IsFolderZip bool
	StoragePath string
}

// FileRepository 定义 File 模块核心查询访问接口。
type FileRepository interface {
	SearchFiles(ctx context.Context, uid int64, q FileSearchQuery) ([]model.File, int64, error)
	ResolveValidShare(ctx context.Context, code string) (model.Share, model.File, error)
	CollectBatchDownloadEntries(ctx context.Context, uid int64, fileIDs []int64) ([]BatchDownloadEntry, error)
}
