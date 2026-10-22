package contract

import (
	"context"
	"time"

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
	File          model.File
	IsFolderZip   bool
	StoragePath   string
	FolderEntries []FolderZipEntry
}

type FolderZipEntry struct {
	RelPath     string
	StoragePath string
	IsDir       bool
}

// FileOperationLogQuery 定义操作日志查询参数。
type FileOperationLogQuery struct {
	Action    string
	Status    string
	StartTime string
	EndTime   string
	Page      int
	Limit     int
	All       int
}

// UploadCommit reports whether this request's unique candidate can be discarded.
// An uncertain commit leaves it retained for reconciliation.
type UploadCommit struct {
	File                model.File
	CandidateDisposable bool
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
	ResolveFileVersion(ctx context.Context, uid, fileID int64, version int) (model.File, model.Storage, error)
	GetStorageByID(ctx context.Context, storageID int64) (model.Storage, error)
	CollectFolderZipEntries(ctx context.Context, uid, rootID int64) ([]FolderZipEntry, error)
	GetSystemSettingByKey(ctx context.Context, key string) (model.SystemSetting, error)
	CreateFolderRecord(ctx context.Context, uid int64, name string, parentID int64) (model.File, error)
	RenameFile(ctx context.Context, uid, fileID int64, name string) (model.File, error)
	CountActiveChildren(ctx context.Context, parentID int64) (int64, error)
	SoftDeleteFileWithLivePhotoCleanup(ctx context.Context, uid, fileID int64) error
	MoveFiles(ctx context.Context, uid int64, fileIDs []int64, targetParentID int64) ([]int64, error)
	CopyFiles(ctx context.Context, uid int64, fileIDs []int64, targetParentID int64) ([]int64, error)
	RestoreFromRecycleBin(ctx context.Context, uid int64, fileID int64) (model.File, error)
	PermanentDeleteFromRecycleBin(ctx context.Context, uid int64, fileID int64, clientIP string) error
	GetUploadTaskByUploadID(ctx context.Context, uploadID string) (model.UploadTask, error)
	GetUploadTaskAndUploadedChunkIndexes(ctx context.Context, userID int64, uploadID string) (model.UploadTask, []int, error)
	CreateUploadTask(ctx context.Context, task model.UploadTask) error
	UpsertUploadChunkAndRefreshProgress(ctx context.Context, taskID int64, uploadID string, chunkIndex int, chunkSize int64, chunkPath string) error
	CountDoneUploadChunks(ctx context.Context, uploadID string) (int64, error)
	CompleteUploadPersist(ctx context.Context, uid int64, parentID int64, uploadID string, task model.UploadTask, totalChunks int, storagePath string, mergedSize int64, fileHash string, clientIP string) (UploadCommit, error)
	CreateShare(ctx context.Context, uid int64, fileID int64, shareCode string, expireAt *time.Time) (model.Share, error)
	CreateOperationLog(ctx context.Context, userID int64, fileID *int64, action string, status string, detail string, clientIP string) error
	ListShares(ctx context.Context, uid int64) ([]model.Share, error)
	RevokeShare(ctx context.Context, uid int64, code string) error
	ListOperationLogs(ctx context.Context, uid int64, isAdmin bool, q FileOperationLogQuery) ([]model.OperationLog, int64, error)
	ExportOperationLogs(ctx context.Context, uid int64, isAdmin bool, q FileOperationLogQuery) ([]model.OperationLog, error)
	FindLivePhotoAutoBindCandidate(ctx context.Context, parentID int64, excludeID int64, baseName string, needVideo bool) (*model.File, error)
	CreateLivePhotoAutoBindIfAbsent(ctx context.Context, userID int64, imageID int64, videoID int64, clientIP string) error
}
