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
	ListFiles(ctx context.Context, uid int64, parentID int64) ([]model.File, error)
	ListRecycleBin(ctx context.Context, uid int64) ([]model.File, error)
	CanAccessFile(ctx context.Context, userID int64, fileID int64, requireWrite bool) (bool, error)
	GetActiveFileByID(ctx context.Context, fileID int64) (model.File, error)
	GetStorageByID(ctx context.Context, storageID int64) (model.Storage, error)
	HasActiveNameConflict(ctx context.Context, parentID int64, name string, excludeID *int64) (bool, error)
	CreateFolderRecord(ctx context.Context, uid int64, name string, parentID int64) (model.File, error)
	UpdateFileName(ctx context.Context, fileID int64, name string) error
	CountActiveChildren(ctx context.Context, parentID int64) (int64, error)
	SoftDeleteFileWithLivePhotoCleanup(ctx context.Context, fileID int64) error
	MoveFiles(ctx context.Context, uid int64, fileIDs []int64, targetParentID int64) ([]int64, error)
	CopyFiles(ctx context.Context, uid int64, fileIDs []int64, targetParentID int64) ([]int64, error)
	RestoreFromRecycleBin(ctx context.Context, uid int64, fileID int64) (model.File, error)
	PermanentDeleteFromRecycleBin(ctx context.Context, uid int64, fileID int64) error
	GetUploadTaskByUploadID(ctx context.Context, uploadID string) (model.UploadTask, error)
	GetUploadTaskAndUploadedChunkIndexes(ctx context.Context, userID int64, uploadID string) (model.UploadTask, []int, error)
	CreateUploadTask(ctx context.Context, task model.UploadTask) error
	UpsertUploadChunkAndRefreshProgress(ctx context.Context, taskID int64, uploadID string, chunkIndex int, chunkSize int64, chunkPath string) error
	CountDoneUploadChunks(ctx context.Context, uploadID string) (int64, error)
	CompleteUploadPersist(ctx context.Context, uid int64, parentID int64, uploadID string, task model.UploadTask, totalChunks int, storagePath string, mergedSize int64, fileHash string) (model.File, error)
}
