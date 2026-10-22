package service

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"cloudenest_go/internal/blob"
	"cloudenest_go/internal/service/contract"
	"cloudenest_go/model"

	"gorm.io/gorm"
)

// FileService 承接 File 模块分阶段迁移的业务逻辑。
type FileService struct {
	fileRepo contract.FileRepository
}

var (
	ErrFilePermissionDenied = errors.New("permission denied")
	ErrFileInvalidParentID  = errors.New("invalid parent_id")
	ErrFileParentNotFound   = errors.New("parent not found")
	ErrFileParentNotFolder  = errors.New("parent is not folder")
	ErrFileNameRequired     = errors.New("name is required")
	ErrFileNameConflict     = errors.New("name already exists in this folder")
	ErrFileNotFound         = errors.New("file not found")
	ErrFileFolderNotEmpty   = errors.New("folder is not empty")
	ErrTargetFolderNotFound = errors.New("target folder not found")
	ErrTargetNotFolder      = errors.New("target is not folder")
	ErrCannotMoveToItself   = errors.New("cannot move to itself")
	ErrCannotMoveToDesc     = errors.New("cannot move folder into its descendant")
	ErrRecycleItemNotFound  = errors.New("recycle item not found")
	ErrUploadTaskNotFound   = errors.New("upload task not found")
	ErrUploadTaskCompleted  = errors.New("upload task already completed")
	ErrChunkIndexOutOfRange = errors.New("chunk_index out of range")
	ErrUploadMetaIncomplete = errors.New("upload task metadata incomplete")
	ErrChunksNotComplete    = errors.New("chunks not fully uploaded")
	ErrShareExpired         = errors.New("share expired")
	ErrStorageNotFound      = errors.New("storage record not found")
	ErrPhysicalFileNotFound = errors.New("physical file not found")
)

func NewFileService(fileRepo contract.FileRepository) *FileService {
	return &FileService{fileRepo: fileRepo}
}

// FileOperationLogQuery 表示操作日志查询参数。
type FileOperationLogQuery struct {
	Action    string
	Status    string
	StartTime string
	EndTime   string
	Page      int
	Limit     int
	All       int
}

// FileCreateShareResult 表示创建分享结果。
type FileCreateShareResult struct {
	ShareCode  string
	ExpireTime *time.Time
}

// FileOperationLogListResult 表示操作日志列表结果。
type FileOperationLogListResult struct {
	Items []model.OperationLog
	Total int64
}

// FileSearchQuery 表示文件检索参数。
type FileSearchQuery struct {
	ParentID  int64
	Name      string
	FileType  string
	StartTime string
	EndTime   string
	Page      int
	Limit     int
}

// FileSearchResult 表示文件检索结果。
type FileSearchResult struct {
	Items []model.File
	Total int64
}

// FileUploadInitInput 表示上传任务初始化参数。
type FileUploadInitInput struct {
	UploadID    string
	ParentID    int64
	Filename    string
	TotalSize   int64
	ChunkSize   int
	TotalChunks int
}

// FileUploadInitResult 表示上传初始化返回。
type FileUploadInitResult struct {
	UploadID       string
	UploadedChunks []int
	UploadedCount  int
	Status         int8
}

// FileUploadProgressResult 表示上传进度。
type FileUploadProgressResult struct {
	UploadID       string
	Status         int8
	UploadedCount  int
	TotalChunks    int
	UploadedChunks []int
	MissingChunks  []int
	Progress       float64
}

// FileUploadTaskSnapshot 表示完成上传前的任务快照。
type FileUploadTaskSnapshot struct {
	Task       model.UploadTask
	TotalSize  int64
	ChunkSize  int
	TotalChunk int
}

// FileBatchDownloadEntry 表示批量下载中一个可下载条目。
type FileBatchDownloadEntry = contract.BatchDownloadEntry

// FileFolderZipEntry 表示目录打包中的单个条目。
type FileFolderZipEntry = contract.FolderZipEntry

// ListFiles 返回指定目录下可读文件列表。
func (s *FileService) ListFiles(ctx context.Context, uid int64, parentID int64) ([]model.File, error) {
	if parentID < 0 {
		return nil, ErrFileInvalidParentID
	}

	if parentID > 0 {
		canRead, err := s.canAccessFile(ctx, uid, parentID, false)
		if err != nil {
			return nil, err
		}
		if !canRead {
			return nil, ErrFilePermissionDenied
		}
	}

	rows, err := s.fileRepo.ListFiles(ctx, uid, parentID)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// CreateFolder 创建目录。
func (s *FileService) CreateFolder(ctx context.Context, uid int64, name string, parentID int64, clientIP string) (model.File, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return model.File{}, ErrFileNameRequired
	}
	if parentID < 0 {
		return model.File{}, ErrFileInvalidParentID
	}

	if parentID > 0 {
		canWrite, err := s.canAccessFile(ctx, uid, parentID, true)
		if err != nil {
			return model.File{}, err
		}
		if !canWrite {
			return model.File{}, ErrFilePermissionDenied
		}

		parent, err := s.fileRepo.GetActiveFileByID(ctx, parentID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return model.File{}, ErrFileParentNotFound
			}
			return model.File{}, err
		}
		if parent.IsFolder != 1 {
			return model.File{}, ErrFileParentNotFolder
		}
	}

	folder, err := s.fileRepo.CreateFolderRecord(ctx, uid, name, parentID)
	if err != nil {
		return model.File{}, translateFileMutationError(err)
	}
	_ = s.logOperation(ctx, uid, &folder.ID, "create_folder", "success", fmt.Sprintf("parent_id=%d", parentID), clientIP)
	return folder, nil
}

// RenameFile 重命名文件或目录。
func (s *FileService) RenameFile(ctx context.Context, uid int64, fileID int64, name string, clientIP string) (model.File, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return model.File{}, ErrFileNameRequired
	}

	canWrite, err := s.canAccessFile(ctx, uid, fileID, true)
	if err != nil {
		return model.File{}, err
	}
	if !canWrite {
		return model.File{}, ErrFilePermissionDenied
	}

	f, err := s.fileRepo.GetActiveFileByID(ctx, fileID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.File{}, ErrFileNotFound
		}
		return model.File{}, err
	}

	renamed, err := s.fileRepo.RenameFile(ctx, uid, f.ID, name)
	if err != nil {
		return model.File{}, translateFileMutationError(err)
	}
	f = renamed
	_ = s.logOperation(ctx, uid, &f.ID, "rename", "success", "renamed file", clientIP)
	return f, nil
}

// DeleteFile 逻辑删除文件或目录。
func (s *FileService) DeleteFile(ctx context.Context, uid int64, fileID int64, clientIP string) error {
	canWrite, err := s.canAccessFile(ctx, uid, fileID, true)
	if err != nil {
		return err
	}
	if !canWrite {
		return ErrFilePermissionDenied
	}

	f, err := s.fileRepo.GetActiveFileByID(ctx, fileID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrFileNotFound
		}
		return err
	}

	if f.IsFolder == 1 {
		childCount, err := s.fileRepo.CountActiveChildren(ctx, fileID)
		if err != nil {
			return err
		}
		if childCount > 0 {
			return ErrFileFolderNotEmpty
		}
	}

	if err := s.fileRepo.SoftDeleteFileWithLivePhotoCleanup(ctx, uid, f.ID); err != nil {
		return translateFileMutationError(err)
	}
	_ = s.logOperation(ctx, uid, &f.ID, "delete", "success", "moved to recycle bin", clientIP)
	return nil
}

// InitUpload 初始化上传任务，支持续传查询。
func (s *FileService) InitUpload(ctx context.Context, uid int64, in FileUploadInitInput) (FileUploadInitResult, error) {
	in.Filename = strings.TrimSpace(in.Filename)
	if err := contract.ValidateFileName(in.Filename); err != nil {
		return FileUploadInitResult{}, err
	}
	in.UploadID = strings.TrimSpace(in.UploadID)
	if in.Filename == "" || in.TotalSize <= 0 || in.ParentID < 0 {
		return FileUploadInitResult{}, fmt.Errorf("invalid upload parameters")
	}

	if in.ChunkSize < 0 || in.TotalChunks < 0 {
		return FileUploadInitResult{}, contract.ErrInvalidInput
	}
	if in.ChunkSize <= 0 {
		if v, ok, err := s.getSystemSettingInt64(ctx, "upload_default_chunk_size_mb"); err != nil {
			return FileUploadInitResult{}, err
		} else if ok && v > 0 {
			if v > 1024 {
				return FileUploadInitResult{}, contract.ErrQuotaAccounting
			}
			in.ChunkSize = int(v * 1024 * 1024)
		}
	}
	if in.ChunkSize <= 0 {
		in.ChunkSize = 4 * 1024 * 1024
	}

	if in.ChunkSize > math.MaxInt32 {
		return FileUploadInitResult{}, contract.ErrInvalidInput
	}
	calculated := 1 + (in.TotalSize-1)/int64(in.ChunkSize)
	if calculated > math.MaxInt32 {
		return FileUploadInitResult{}, contract.ErrInvalidInput
	}
	calculatedTotalChunks := int(calculated)
	if in.TotalChunks <= 0 {
		in.TotalChunks = calculatedTotalChunks
	}
	if in.TotalChunks != calculatedTotalChunks {
		return FileUploadInitResult{}, contract.ErrInvalidInput
	}

	if in.ParentID > 0 {
		canWrite, err := s.canAccessFile(ctx, uid, in.ParentID, true)
		if err != nil {
			return FileUploadInitResult{}, err
		}
		if !canWrite {
			return FileUploadInitResult{}, ErrFilePermissionDenied
		}
	}

	if in.UploadID != "" {
		task, chunkIndexes, err := s.fileRepo.GetUploadTaskAndUploadedChunkIndexes(ctx, uid, in.UploadID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return FileUploadInitResult{}, ErrUploadTaskNotFound
			}
			return FileUploadInitResult{}, err
		}
		if task.ParentID != in.ParentID || task.Filename != in.Filename || task.TotalSize == nil || *task.TotalSize != in.TotalSize || task.ChunkSize == nil || *task.ChunkSize != in.ChunkSize || task.TotalChunks == nil || *task.TotalChunks != in.TotalChunks {
			return FileUploadInitResult{}, contract.ErrUploadContract
		}
		return FileUploadInitResult{
			UploadID:       task.UploadID,
			UploadedChunks: chunkIndexes,
			UploadedCount:  task.UploadedChunks,
			Status:         task.Status,
		}, nil
	}

	uploadID := generateUploadID(uid)
	uidCopy := uid
	totalSizeCopy := in.TotalSize
	chunkSizeCopy := in.ChunkSize
	totalChunksCopy := in.TotalChunks
	task := model.UploadTask{
		ContractVersion: 1, ReservedBytes: in.TotalSize, ParentID: in.ParentID,
		UploadID:    uploadID,
		UserID:      &uidCopy,
		Filename:    in.Filename,
		TotalSize:   &totalSizeCopy,
		ChunkSize:   &chunkSizeCopy,
		TotalChunks: &totalChunksCopy,
		Status:      0,
	}
	if err := s.fileRepo.CreateUploadTask(ctx, task); err != nil {
		return FileUploadInitResult{}, err
	}
	return FileUploadInitResult{UploadID: uploadID, UploadedChunks: []int{}, UploadedCount: 0, Status: 0}, nil
}

// ValidateUploadChunk 校验分片上传任务归属与状态。
func (s *FileService) ValidateUploadChunk(ctx context.Context, uid int64, uploadID string, chunkIndex int) (model.UploadTask, error) {
	task, err := s.fileRepo.GetUploadTaskByUploadID(ctx, uid, uploadID)
	if err != nil {
		if errors.Is(err, contract.ErrForbidden) {
			return model.UploadTask{}, ErrFilePermissionDenied
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.UploadTask{}, ErrUploadTaskNotFound
		}
		return model.UploadTask{}, err
	}
	if task.UserID == nil || *task.UserID != uid {
		return model.UploadTask{}, ErrFilePermissionDenied
	}
	if task.Status == 1 {
		return model.UploadTask{}, ErrUploadTaskCompleted
	}
	if task.Status != 0 {
		return model.UploadTask{}, contract.ErrUploadContract
	}
	if task.TotalChunks != nil && chunkIndex >= *task.TotalChunks {
		return model.UploadTask{}, ErrChunkIndexOutOfRange
	}
	return task, nil
}

// MarkChunkUploaded 记录分片上传完成并刷新已上传分片数。
func (s *FileService) MarkChunkUploaded(ctx context.Context, task model.UploadTask, uploadID string, chunkIndex int, chunkSize int64, chunkPath string) error {
	return s.fileRepo.UpsertUploadChunkAndRefreshProgress(ctx, task.ID, uploadID, chunkIndex, chunkSize, chunkPath)
}

// PrepareCompleteUpload 校验可完成条件并返回任务快照。
func (s *FileService) PrepareCompleteUpload(ctx context.Context, uid int64, uploadID string, parentID int64) (FileUploadTaskSnapshot, error) {
	if parentID < 0 || strings.TrimSpace(uploadID) == "" {
		return FileUploadTaskSnapshot{}, fmt.Errorf("invalid parameters")
	}

	if parentID > 0 {
		canWrite, err := s.canAccessFile(ctx, uid, parentID, true)
		if err != nil {
			return FileUploadTaskSnapshot{}, err
		}
		if !canWrite {
			return FileUploadTaskSnapshot{}, ErrFilePermissionDenied
		}
	}

	task, err := s.fileRepo.GetUploadTaskByUploadID(ctx, uid, uploadID)
	if err != nil {
		if errors.Is(err, contract.ErrForbidden) {
			return FileUploadTaskSnapshot{}, ErrFilePermissionDenied
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return FileUploadTaskSnapshot{}, ErrUploadTaskNotFound
		}
		return FileUploadTaskSnapshot{}, err
	}
	if task.UserID == nil || *task.UserID != uid {
		return FileUploadTaskSnapshot{}, ErrFilePermissionDenied
	}
	if task.Status == 1 {
		return FileUploadTaskSnapshot{}, ErrUploadTaskCompleted
	}
	if task.Status != 0 {
		return FileUploadTaskSnapshot{}, contract.ErrUploadContract
	}
	if task.ParentID != parentID {
		return FileUploadTaskSnapshot{}, contract.ErrUploadContract
	}
	if task.TotalChunks == nil || task.ChunkSize == nil || task.TotalSize == nil {
		return FileUploadTaskSnapshot{}, ErrUploadMetaIncomplete
	}

	doneCount, err := s.fileRepo.CountDoneUploadChunks(ctx, uploadID)
	if err != nil {
		return FileUploadTaskSnapshot{}, err
	}
	if int(doneCount) != *task.TotalChunks {
		return FileUploadTaskSnapshot{}, ErrChunksNotComplete
	}

	return FileUploadTaskSnapshot{Task: task, TotalSize: *task.TotalSize, ChunkSize: *task.ChunkSize, TotalChunk: *task.TotalChunks}, nil
}

// CompleteUpload 完成上传后写入 storage/file 及任务状态。
func (s *FileService) CompleteUpload(ctx context.Context, uid int64, parentID int64, uploadID string, snapshot FileUploadTaskSnapshot, storagePath, storageRoot string, mergedSize int64, fileHash string, clientIP string) (contract.UploadCommit, error) {
	return s.fileRepo.CompleteUploadPersist(ctx, uid, parentID, uploadID, snapshot.Task, snapshot.TotalChunk, storagePath, storageRoot, mergedSize, fileHash, clientIP)
}

func (s *FileService) ResolveFileVersion(ctx context.Context, uid, fileID int64, version int) (model.File, model.Storage, error) {
	file, storage, err := s.fileRepo.ResolveFileVersion(ctx, uid, fileID, version)
	if err != nil {
		return model.File{}, model.Storage{}, err
	}
	storage, err = blob.Pin(ctx, storage)
	if err != nil {
		return model.File{}, model.Storage{}, err
	}
	if err := blob.VerifyStorageObject(ctx, storage); err != nil {
		return model.File{}, model.Storage{}, err
	}
	return file, storage, nil
}

// UploadProgress 查询上传进度。
func (s *FileService) UploadProgress(ctx context.Context, uid int64, uploadID string) (FileUploadProgressResult, error) {
	var (
		task           model.UploadTask
		uploadedChunks []int
		err            error
	)
	task, uploadedChunks, err = s.fileRepo.GetUploadTaskAndUploadedChunkIndexes(ctx, uid, uploadID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return FileUploadProgressResult{}, ErrUploadTaskNotFound
		}
		return FileUploadProgressResult{}, err
	}

	totalChunks := 0
	if task.TotalChunks != nil {
		totalChunks = *task.TotalChunks
	}

	missingChunks := make([]int, 0)
	if totalChunks > 0 {
		uploadedSet := make(map[int]bool, len(uploadedChunks))
		for _, idx := range uploadedChunks {
			uploadedSet[idx] = true
		}
		for i := 0; i < totalChunks; i++ {
			if !uploadedSet[i] {
				missingChunks = append(missingChunks, i)
			}
		}
	}

	progress := 0.0
	if totalChunks > 0 {
		progress = float64(len(uploadedChunks)) * 100 / float64(totalChunks)
	}

	return FileUploadProgressResult{UploadID: task.UploadID, Status: task.Status, UploadedCount: len(uploadedChunks), TotalChunks: totalChunks, UploadedChunks: uploadedChunks, MissingChunks: missingChunks, Progress: progress}, nil
}

// GetAccessibleFile 查询可访问文件（不包含 storage 校验）。
func (s *FileService) GetAccessibleFile(ctx context.Context, uid int64, fileID int64, requireWrite bool) (model.File, error) {
	canAccess, err := s.canAccessFile(ctx, uid, fileID, requireWrite)
	if err != nil {
		return model.File{}, err
	}
	if !canAccess {
		return model.File{}, ErrFilePermissionDenied
	}

	f, err := s.fileRepo.GetActiveFileByID(ctx, fileID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.File{}, ErrFileNotFound
		}
		return model.File{}, err
	}
	return f, nil
}

// GetAccessibleFileAndStorage 查询可访问文件及其存储记录。
func (s *FileService) GetAccessibleFileAndStorage(ctx context.Context, uid int64, fileID int64, requireWrite bool) (model.File, model.Storage, error) {
	f, err := s.GetAccessibleFile(ctx, uid, fileID, requireWrite)
	if err != nil {
		return model.File{}, model.Storage{}, err
	}
	if f.StorageID == nil {
		return f, model.Storage{}, gorm.ErrRecordNotFound
	}

	storage, err := s.fileRepo.GetStorageByID(ctx, *f.StorageID)
	if err != nil {
		return model.File{}, model.Storage{}, err
	}
	storage, err = blob.Pin(ctx, storage)
	if err != nil {
		return model.File{}, model.Storage{}, err
	}
	if _, err := os.Stat(storage.FilePath); err != nil {
		return model.File{}, model.Storage{}, os.ErrNotExist
	}
	return f, storage, nil
}

// MoveFiles 批量移动文件或文件夹到目标目录。
func (s *FileService) MoveFiles(ctx context.Context, uid int64, fileIDs []int64, targetParentID int64, clientIP string) ([]int64, error) {
	if targetParentID < 0 {
		return nil, ErrFileInvalidParentID
	}

	updated, err := s.fileRepo.MoveFiles(ctx, uid, fileIDs, targetParentID)
	if err != nil {
		switch {
		case errors.Is(err, contract.ErrForbidden):
			return nil, ErrFilePermissionDenied
		case errors.Is(err, contract.ErrTargetNotFound):
			return nil, ErrTargetFolderNotFound
		case errors.Is(err, contract.ErrTargetNotFolder):
			return nil, ErrTargetNotFolder
		case errors.Is(err, contract.ErrCannotMoveToItself):
			return nil, ErrCannotMoveToItself
		case errors.Is(err, contract.ErrCannotMoveToDesc):
			return nil, ErrCannotMoveToDesc
		case errors.Is(err, contract.ErrNotFound):
			return nil, ErrFileNotFound
		default:
			return nil, err
		}
	}

	_ = s.logOperation(ctx, uid, nil, "move", "success", fmt.Sprintf("count=%d,target_parent=%d", len(updated), targetParentID), clientIP)
	return updated, nil
}

// CopyFiles 批量复制文件或文件夹。
func (s *FileService) CopyFiles(ctx context.Context, uid int64, fileIDs []int64, targetParentID int64, clientIP string) ([]int64, error) {
	if targetParentID < 0 {
		return nil, ErrFileInvalidParentID
	}

	newIDs, err := s.fileRepo.CopyFiles(ctx, uid, fileIDs, targetParentID)
	if err != nil {
		switch {
		case errors.Is(err, contract.ErrForbidden):
			return nil, ErrFilePermissionDenied
		case errors.Is(err, contract.ErrNotFound):
			return nil, ErrFileNotFound
		default:
			return nil, err
		}
	}

	_ = s.logOperation(ctx, uid, nil, "copy", "success", fmt.Sprintf("count=%d,target_parent=%d", len(newIDs), targetParentID), clientIP)
	return newIDs, nil
}

// ListRecycleBin 获取当前用户回收站列表。
func (s *FileService) ListRecycleBin(ctx context.Context, uid int64) ([]model.File, error) {
	return s.fileRepo.ListRecycleBin(ctx, uid)
}

// RestoreFromRecycleBin 恢复回收站条目。
func (s *FileService) RestoreFromRecycleBin(ctx context.Context, uid int64, fileID int64, clientIP string) (model.File, error) {
	restored, err := s.fileRepo.RestoreFromRecycleBin(ctx, uid, fileID)
	if err != nil {
		switch {
		case errors.Is(err, contract.ErrRecycleItemNotFound):
			return model.File{}, ErrRecycleItemNotFound
		case errors.Is(err, contract.ErrForbidden):
			return model.File{}, ErrFilePermissionDenied
		default:
			return model.File{}, err
		}
	}

	_ = s.logOperation(ctx, uid, &restored.ID, "restore", "success", "restore from recycle bin", clientIP)
	return restored, nil
}

// PermanentDeleteFromRecycleBin 彻底删除回收站条目。
func (s *FileService) PermanentDeleteFromRecycleBin(ctx context.Context, uid int64, fileID int64, clientIP string) error {
	err := s.fileRepo.PermanentDeleteFromRecycleBin(ctx, uid, fileID, clientIP)
	if err != nil {
		switch {
		case errors.Is(err, contract.ErrRecycleItemNotFound):
			return ErrRecycleItemNotFound
		case errors.Is(err, contract.ErrForbidden):
			return ErrFilePermissionDenied
		default:
			return err
		}
	}

	return nil
}

// CreateShare 创建分享链接。
func (s *FileService) CreateShare(ctx context.Context, uid int64, fileID int64, expireHours *int, clientIP string) (FileCreateShareResult, error) {
	canRead, err := s.canAccessFile(ctx, uid, fileID, false)
	if err != nil {
		return FileCreateShareResult{}, err
	}
	if !canRead {
		return FileCreateShareResult{}, fmt.Errorf("permission denied")
	}

	finalExpireHours := int64(0)
	if expireHours != nil {
		finalExpireHours = int64(*expireHours)
	} else {
		if v, ok, err := s.getSystemSettingInt64(ctx, "share_default_expire_hours"); err != nil {
			return FileCreateShareResult{}, err
		} else if ok {
			finalExpireHours = v
		}
	}
	if finalExpireHours < 0 || finalExpireHours > 24*365 {
		return FileCreateShareResult{}, fmt.Errorf("invalid expire_hours")
	}

	shareCode, err := generateShareCode(12)
	if err != nil {
		return FileCreateShareResult{}, err
	}

	fileIDCopy := fileID
	uidCopy := uid
	var expireAt *time.Time
	if finalExpireHours > 0 {
		t := time.Now().Add(time.Duration(finalExpireHours) * time.Hour)
		expireAt = &t
	}

	share := model.Share{FileID: &fileIDCopy, UserID: &uidCopy, ShareCode: shareCode, ExpireTime: expireAt}
	repoShare, err := s.fileRepo.CreateShare(ctx, uid, fileID, shareCode, expireAt)
	if err != nil {
		return FileCreateShareResult{}, err
	}
	share = repoShare

	_ = s.logOperation(ctx, uid, &fileID, "create_share", "success", fmt.Sprintf("share_code=%s", shareCode), clientIP)
	return FileCreateShareResult{ShareCode: share.ShareCode, ExpireTime: share.ExpireTime}, nil
}

// ListShares 查询当前用户的分享记录。
func (s *FileService) ListShares(ctx context.Context, uid int64) ([]model.Share, error) {
	return s.fileRepo.ListShares(ctx, uid)
}

// RevokeShare 撤销指定分享。
func (s *FileService) RevokeShare(ctx context.Context, uid int64, code string, clientIP string) error {
	if err := s.fileRepo.RevokeShare(ctx, uid, code); err != nil {
		return err
	}
	_ = s.logOperation(ctx, uid, nil, "revoke_share", "success", fmt.Sprintf("share_code=%s", code), clientIP)
	return nil
}

// GetPublicShareInfo 获取公开分享的分享与文件信息。
func (s *FileService) GetPublicShareInfo(ctx context.Context, code string) (model.Share, model.File, error) {
	return s.resolveValidShare(ctx, code)
}

// ResolvePublicShareDownload 获取公开分享下载目标（文件或目录）。
func (s *FileService) ResolvePublicShareDownload(ctx context.Context, code string) (model.File, *model.Storage, int64, error) {
	share, file, err := s.resolveValidShare(ctx, code)
	if err != nil {
		return model.File{}, nil, 0, err
	}
	if file.IsFolder == 1 {
		return file, nil, *share.UserID, nil
	}
	if file.StorageID == nil {
		return model.File{}, nil, 0, ErrStorageNotFound
	}

	storage, storageErr := s.fileRepo.GetStorageByID(ctx, *file.StorageID)
	if storageErr != nil {
		if errors.Is(storageErr, gorm.ErrRecordNotFound) {
			return model.File{}, nil, 0, ErrStorageNotFound
		}
		return model.File{}, nil, 0, storageErr
	}
	storage, storageErr = blob.Pin(ctx, storage)
	if storageErr != nil {
		return model.File{}, nil, 0, storageErr
	}
	if _, err := os.Stat(storage.FilePath); err != nil {
		return model.File{}, nil, 0, ErrPhysicalFileNotFound
	}

	return file, &storage, *share.UserID, nil
}

// CollectBatchDownloadEntries 在一致快照中验证批量下载清单，任何失败均中止。
func (s *FileService) CollectBatchDownloadEntries(ctx context.Context, uid int64, fileIDs []int64) ([]FileBatchDownloadEntry, error) {
	entries, err := s.fileRepo.CollectBatchDownloadEntries(ctx, uid, fileIDs)
	if err != nil {
		return nil, err
	}
	for i := range entries {
		entry := &entries[i]
		if entry.IsFolderZip {
			if err := s.pinArchiveEntries(ctx, entry.FolderEntries); err != nil {
				return nil, err
			}
		} else {
			if entry.File.StorageID == nil {
				return nil, gorm.ErrRecordNotFound
			}
			path, err := s.pinnedRegularPath(ctx, *entry.File.StorageID)
			if err != nil {
				return nil, err
			}
			entry.StoragePath = path
		}
	}
	return entries, nil
}

// SearchFiles 按名称/类型/时间进行检索和筛选。
func (s *FileService) SearchFiles(ctx context.Context, uid int64, q FileSearchQuery) (FileSearchResult, error) {
	items, total, err := s.fileRepo.SearchFiles(ctx, uid, contract.FileSearchQuery{
		ParentID:  q.ParentID,
		Name:      q.Name,
		FileType:  q.FileType,
		StartTime: q.StartTime,
		EndTime:   q.EndTime,
		Page:      q.Page,
		Limit:     q.Limit,
	})
	if err != nil {
		return FileSearchResult{}, err
	}
	return FileSearchResult{Items: items, Total: total}, nil
}

// ListOperationLogs 查询操作日志。
func (s *FileService) ListOperationLogs(ctx context.Context, uid int64, isAdmin bool, q FileOperationLogQuery) (FileOperationLogListResult, error) {
	rows, total, err := s.fileRepo.ListOperationLogs(ctx, uid, isAdmin, contract.FileOperationLogQuery{
		Action:    q.Action,
		Status:    q.Status,
		StartTime: q.StartTime,
		EndTime:   q.EndTime,
		Page:      q.Page,
		Limit:     q.Limit,
		All:       q.All,
	})
	if err != nil {
		return FileOperationLogListResult{}, err
	}
	return FileOperationLogListResult{Items: rows, Total: total}, nil
}

// ExportOperationLogs 导出操作日志查询（返回行数据，由 handler 输出 CSV）。
func (s *FileService) ExportOperationLogs(ctx context.Context, uid int64, isAdmin bool, q FileOperationLogQuery) ([]model.OperationLog, error) {
	return s.fileRepo.ExportOperationLogs(ctx, uid, isAdmin, contract.FileOperationLogQuery{
		Action:    q.Action,
		Status:    q.Status,
		StartTime: q.StartTime,
		EndTime:   q.EndTime,
		Page:      q.Page,
		Limit:     q.Limit,
		All:       q.All,
	})
}

// GetStorageByID 按存储 ID 获取存储记录。
func (s *FileService) GetStorageByID(ctx context.Context, storageID int64) (model.Storage, error) {
	if storageID <= 0 {
		return model.Storage{}, ErrStorageNotFound
	}

	storage, err := s.fileRepo.GetStorageByID(ctx, storageID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.Storage{}, ErrStorageNotFound
		}
		return model.Storage{}, err
	}
	return blob.Pin(ctx, storage)
}

// RecordOperation 写入操作日志。
func (s *FileService) RecordOperation(ctx context.Context, userID int64, fileID *int64, action string, status string, detail string, clientIP string) error {
	return s.logOperation(ctx, userID, fileID, action, status, detail, clientIP)
}

// BatchDeleteFiles 批量删除文件或目录（移动到回收站）。
func (s *FileService) BatchDeleteFiles(ctx context.Context, uid int64, fileIDs []int64, clientIP string) ([]int64, map[string]string) {
	ids := uniqueInt64InService(fileIDs)
	deletedIDs := make([]int64, 0, len(ids))
	failed := make(map[string]string)

	for _, fileID := range ids {
		if fileID <= 0 {
			failed[strconv.FormatInt(fileID, 10)] = "invalid file id"
			continue
		}

		err := s.DeleteFile(ctx, uid, fileID, clientIP)
		if err != nil {
			switch {
			case errors.Is(err, ErrFilePermissionDenied):
				failed[strconv.FormatInt(fileID, 10)] = "permission denied"
			case errors.Is(err, ErrFileNotFound):
				failed[strconv.FormatInt(fileID, 10)] = "file not found"
			case errors.Is(err, ErrFileFolderNotEmpty):
				failed[strconv.FormatInt(fileID, 10)] = "folder is not empty"
			default:
				failed[strconv.FormatInt(fileID, 10)] = "delete failed"
			}
			continue
		}

		deletedIDs = append(deletedIDs, fileID)
	}

	_ = s.logOperation(ctx, uid, nil, "batch_delete", "success", fmt.Sprintf("requested=%d,deleted=%d", len(ids), len(deletedIDs)), clientIP)
	return deletedIDs, failed
}

// CollectFolderZipEntries 收集目录下载所需的 zip 条目。
func (s *FileService) CollectFolderZipEntries(ctx context.Context, uid, rootID int64) ([]FileFolderZipEntry, error) {
	entries, err := s.fileRepo.CollectFolderZipEntries(ctx, uid, rootID)
	if err != nil {
		return nil, err
	}
	if err := s.pinArchiveEntries(ctx, entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// TryAutoBindLivePhotoForFile 尝试将当前文件与同目录同名异类型文件自动绑定。
func (s *FileService) TryAutoBindLivePhotoForFile(ctx context.Context, userID int64, file model.File, clientIP string) error {
	if file.IsFolder == 1 || file.IsDeleted == 1 {
		return nil
	}

	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(file.Filename), "."))
	isImage := isLivePhotoImageExtInService(ext)
	isVideo := isLivePhotoVideoExtInService(ext)
	if !isImage && !isVideo {
		return nil
	}

	base := strings.TrimSpace(strings.TrimSuffix(file.Filename, filepath.Ext(file.Filename)))
	if base == "" {
		return nil
	}

	var (
		matched *model.File
		err     error
	)
	matched, err = s.fileRepo.FindLivePhotoAutoBindCandidate(ctx, file.ParentID, file.ID, base, isImage)
	if err != nil {
		return err
	}
	if matched == nil {
		return nil
	}

	canWriteSelf, err := s.canAccessFile(ctx, userID, file.ID, true)
	if err != nil || !canWriteSelf {
		return err
	}
	canWriteOther, err := s.canAccessFile(ctx, userID, matched.ID, true)
	if err != nil || !canWriteOther {
		return err
	}

	imageID := file.ID
	videoID := matched.ID
	if isVideo {
		imageID = matched.ID
		videoID = file.ID
	}

	return s.fileRepo.CreateLivePhotoAutoBindIfAbsent(ctx, userID, imageID, videoID, clientIP)
}

// GetSystemSettingInt64 读取整数配置项。
func (s *FileService) GetSystemSettingInt64(ctx context.Context, key string) (int64, bool, error) {
	return s.getSystemSettingInt64(ctx, key)
}

// GetSystemSettingString 读取字符串配置项。
func (s *FileService) GetSystemSettingString(ctx context.Context, key string) (string, bool, error) {
	return s.getSystemSettingString(ctx, key)
}

// IsInPeakWindow 判断当前请求是否处于高峰时段。
func (s *FileService) IsInPeakWindow(ctx context.Context, uid int64) (bool, error) {
	enabled, start, end, err := s.ResolvePeakWindowSetting(ctx, uid)
	if err != nil {
		return false, err
	}
	if !enabled {
		return false, nil
	}
	startMin, okStart := parseHHMMToMinuteInService(start)
	endMin, okEnd := parseHHMMToMinuteInService(end)
	if !okStart || !okEnd {
		return false, nil
	}

	now := time.Now()
	nowMin := now.Hour()*60 + now.Minute()
	if startMin == endMin {
		return true, nil
	}
	if startMin < endMin {
		return nowMin >= startMin && nowMin < endMin, nil
	}
	return nowMin >= startMin || nowMin < endMin, nil
}

// ResolvePeakWindowSetting 解析用户或全局高峰时段配置。
func (s *FileService) ResolvePeakWindowSetting(ctx context.Context, uid int64) (bool, string, string, error) {
	if uid > 0 {
		enabledKey := "peak_window_enabled_user_" + strconv.FormatInt(uid, 10)
		startKey := "peak_window_start_user_" + strconv.FormatInt(uid, 10)
		endKey := "peak_window_end_user_" + strconv.FormatInt(uid, 10)

		if enabledVal, exists, err := s.getSystemSettingString(ctx, enabledKey); err != nil {
			return false, "", "", err
		} else if exists {
			enabled := strings.TrimSpace(enabledVal) == "1"
			startVal, _, errStart := s.getSystemSettingString(ctx, startKey)
			if errStart != nil {
				return false, "", "", errStart
			}
			endVal, _, errEnd := s.getSystemSettingString(ctx, endKey)
			if errEnd != nil {
				return false, "", "", errEnd
			}
			return enabled, startVal, endVal, nil
		}
	}

	enabledGlobal, _, err := s.getSystemSettingInt64(ctx, "peak_window_enabled")
	if err != nil {
		return false, "", "", err
	}
	startGlobal, _, err := s.getSystemSettingString(ctx, "peak_window_start")
	if err != nil {
		return false, "", "", err
	}
	endGlobal, _, err := s.getSystemSettingString(ctx, "peak_window_end")
	if err != nil {
		return false, "", "", err
	}
	return enabledGlobal == 1, startGlobal, endGlobal, nil
}

func (s *FileService) canAccessFile(ctx context.Context, userID int64, fileID int64, requireWrite bool) (bool, error) {
	return s.fileRepo.CanAccessFile(ctx, userID, fileID, requireWrite)
}

func (s *FileService) getSystemSettingInt64(ctx context.Context, key string) (int64, bool, error) {
	row, err := s.fileRepo.GetSystemSettingByKey(ctx, key)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return 0, false, nil
		}
		return 0, false, err
	}
	v := strings.TrimSpace(row.SettingValue)
	if v == "" {
		return 0, false, nil
	}
	n, convErr := strconv.ParseInt(v, 10, 64)
	if convErr != nil {
		return 0, false, convErr
	}
	return n, true, nil
}

func (s *FileService) getSystemSettingString(ctx context.Context, key string) (string, bool, error) {
	row, err := s.fileRepo.GetSystemSettingByKey(ctx, key)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return "", false, nil
		}
		return "", false, err
	}
	return strings.TrimSpace(row.SettingValue), true, nil
}

func (s *FileService) logOperation(ctx context.Context, userID int64, fileID *int64, action string, status string, detail string, clientIP string) error {
	if len(detail) > 500 {
		detail = detail[:500]
	}
	return s.fileRepo.CreateOperationLog(ctx, userID, fileID, action, status, detail, clientIP)
}

func generateShareCode(length int) (string, error) {
	const chars = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"
	if length <= 0 {
		length = 12
	}
	buf := make([]byte, length)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	for i := range buf {
		buf[i] = chars[int(buf[i])%len(chars)]
	}
	return string(buf), nil
}

func generateUploadID(userID int64) string {
	return fmt.Sprintf("u%d_%d", userID, time.Now().UnixNano())
}

func (s *FileService) resolveValidShare(ctx context.Context, code string) (model.Share, model.File, error) {
	share, file, err := s.fileRepo.ResolveValidShare(ctx, code)
	if err != nil {
		if errors.Is(err, contract.ErrShareExpired) {
			return model.Share{}, model.File{}, ErrShareExpired
		}
		return model.Share{}, model.File{}, err
	}
	return share, file, nil
}

func isDescendantFolderInService(ctx context.Context, db *gorm.DB, candidateID int64, ancestorID int64) (bool, error) {
	if candidateID <= 0 || ancestorID <= 0 {
		return false, nil
	}

	current := candidateID
	for current > 0 {
		if current == ancestorID {
			return true, nil
		}
		var f model.File
		err := db.WithContext(ctx).Select("id", "parent_id").First(&f, "id = ? AND is_deleted = 0", current).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return false, nil
			}
			return false, err
		}
		if f.ParentID == current {
			return false, nil
		}
		current = f.ParentID
	}

	return false, nil
}

func collectDescendantIDsInService(ctx context.Context, db *gorm.DB, rootID int64) ([]int64, error) {
	ids := make([]int64, 0)
	queue := []int64{rootID}

	for len(queue) > 0 {
		parentID := queue[0]
		queue = queue[1:]

		var children []model.File
		err := db.WithContext(ctx).Select("id", "is_folder").Where("parent_id = ?", parentID).Find(&children).Error
		if err != nil {
			return nil, err
		}

		for _, child := range children {
			ids = append(ids, child.ID)
			if child.IsFolder == 1 {
				queue = append(queue, child.ID)
			}
		}
	}

	return ids, nil
}

func uniqueInt64InService(input []int64) []int64 {
	seen := make(map[int64]bool, len(input))
	result := make([]int64, 0, len(input))
	for _, v := range input {
		if seen[v] {
			continue
		}
		seen[v] = true
		result = append(result, v)
	}
	return result
}

// parseFlexibleTimeForFile 支持多种时间格式，供 FileService 内部统一复用。
func parseFlexibleTimeForFile(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	layouts := []string{
		time.RFC3339,
		"2006-01-02 15:04:05",
		"2006-01-02 15:04",
		"2006-01-02",
	}
	for _, layout := range layouts {
		t, err := time.ParseInLocation(layout, raw, time.Local)
		if err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid time format")
}

func parseHHMMToMinuteInService(v string) (int, bool) {
	v = strings.TrimSpace(v)
	if len(v) != 5 || v[2] != ':' {
		return 0, false
	}
	h, errH := strconv.Atoi(v[:2])
	m, errM := strconv.Atoi(v[3:])
	if errH != nil || errM != nil {
		return 0, false
	}
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

func isLivePhotoImageExtInService(ext string) bool {
	images := map[string]bool{"jpg": true, "jpeg": true, "heic": true}
	return images[ext]
}

func isLivePhotoVideoExtInService(ext string) bool {
	videos := map[string]bool{"mov": true, "mp4": true, "m4v": true}
	return videos[ext]
}

// Consistent transport errors for mutations that recheck state under a DB lock.
func translateFileMutationError(err error) error {
	switch {
	case errors.Is(err, contract.ErrForbidden):
		return ErrFilePermissionDenied
	case errors.Is(err, contract.ErrConflict):
		return ErrFileNameConflict
	case errors.Is(err, contract.ErrTargetNotFound):
		return ErrTargetFolderNotFound
	case errors.Is(err, contract.ErrTargetNotFolder):
		return ErrTargetNotFolder
	case errors.Is(err, contract.ErrFolderNotEmpty):
		return ErrFileFolderNotEmpty
	case errors.Is(err, gorm.ErrRecordNotFound):
		return ErrFileNotFound
	default:
		return err
	}
}

// Metadata/ACL snapshots finish before lease transactions begin. This avoids
// holding one pool connection while waiting for another, including pool size 1.
func (s *FileService) pinArchiveEntries(ctx context.Context, entries []FileFolderZipEntry) error {
	for i := range entries {
		if entries[i].IsDir {
			continue
		}
		path, err := s.pinnedRegularPath(ctx, entries[i].StorageID)
		if err != nil {
			return err
		}
		entries[i].StoragePath = path
	}
	return nil
}
func (s *FileService) pinnedRegularPath(ctx context.Context, id int64) (string, error) {
	storage, err := s.GetStorageByID(ctx, id)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(storage.FilePath)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", contract.ErrConflict
	}
	return storage.FilePath, nil
}

func (s *FileService) CancelUpload(ctx context.Context, uid int64, id, ip string) error {
	return s.fileRepo.CancelUploadTask(ctx, uid, id, ip)
}
