package service

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"cloudenest_go/internal/service/contract"
	"cloudenest_go/model"

	"gorm.io/gorm"
)

// FileService 承接 File 模块分阶段迁移的业务逻辑。
type FileService struct {
	db       *gorm.DB
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

func NewFileService(db *gorm.DB, fileRepo contract.FileRepository) *FileService {
	return &FileService{db: db, fileRepo: fileRepo}
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
type FileBatchDownloadEntry struct {
	File        model.File
	IsFolderZip bool
	StoragePath string
}

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

	var rows []model.File
	err := s.db.WithContext(ctx).
		Table("file f").
		Select("f.*").
		Joins("LEFT JOIN file_permission fp ON fp.file_id = f.id AND fp.user_id = ?", uid).
		Where("f.parent_id = ? AND f.is_deleted = 0", parentID).
		Where("f.user_id = ? OR fp.permission IN ?", uid, []string{"read", "write"}).
		Order("f.is_folder DESC, f.filename ASC").
		Find(&rows).Error
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

		var parent model.File
		if err := s.db.WithContext(ctx).First(&parent, "id = ? AND is_deleted = 0", parentID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return model.File{}, ErrFileParentNotFound
			}
			return model.File{}, err
		}
		if parent.IsFolder != 1 {
			return model.File{}, ErrFileParentNotFolder
		}
	}

	var existed int64
	err := s.db.WithContext(ctx).
		Model(&model.File{}).
		Where("parent_id = ? AND filename = ? AND is_deleted = 0", parentID, name).
		Count(&existed).Error
	if err != nil {
		return model.File{}, err
	}
	if existed > 0 {
		return model.File{}, ErrFileNameConflict
	}

	folder := model.File{UserID: uid, Filename: name, ParentID: parentID, IsFolder: 1}
	if err := s.db.WithContext(ctx).Create(&folder).Error; err != nil {
		return model.File{}, err
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

	var f model.File
	if err := s.db.WithContext(ctx).First(&f, "id = ? AND is_deleted = 0", fileID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.File{}, ErrFileNotFound
		}
		return model.File{}, err
	}

	var existed int64
	err = s.db.WithContext(ctx).
		Model(&model.File{}).
		Where("parent_id = ? AND filename = ? AND is_deleted = 0 AND id <> ?", f.ParentID, name, f.ID).
		Count(&existed).Error
	if err != nil {
		return model.File{}, err
	}
	if existed > 0 {
		return model.File{}, ErrFileNameConflict
	}

	if err := s.db.WithContext(ctx).Model(&f).Update("filename", name).Error; err != nil {
		return model.File{}, err
	}
	f.Filename = name
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

	var f model.File
	if err := s.db.WithContext(ctx).First(&f, "id = ? AND is_deleted = 0", fileID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrFileNotFound
		}
		return err
	}

	if f.IsFolder == 1 {
		var childCount int64
		err := s.db.WithContext(ctx).
			Model(&model.File{}).
			Where("parent_id = ? AND is_deleted = 0", fileID).
			Count(&childCount).Error
		if err != nil {
			return err
		}
		if childCount > 0 {
			return ErrFileFolderNotEmpty
		}
	}

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&f).Update("is_deleted", 1).Error; err != nil {
			return err
		}
		// 文件删除后同步清理 Live Photo 关系，避免悬挂绑定。
		if err := tx.Where("image_file_id IN ? OR video_file_id IN ?", []int64{f.ID}, []int64{f.ID}).Delete(&model.LivePhoto{}).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}

	_ = s.logOperation(ctx, uid, &f.ID, "delete", "success", "moved to recycle bin", clientIP)
	return nil
}

// InitUpload 初始化上传任务，支持续传查询。
func (s *FileService) InitUpload(ctx context.Context, uid int64, in FileUploadInitInput) (FileUploadInitResult, error) {
	in.Filename = strings.TrimSpace(in.Filename)
	in.UploadID = strings.TrimSpace(in.UploadID)
	if in.Filename == "" || in.TotalSize <= 0 || in.ParentID < 0 {
		return FileUploadInitResult{}, fmt.Errorf("invalid upload parameters")
	}

	if in.ChunkSize <= 0 {
		if v, ok, err := s.getSystemSettingInt64(ctx, "upload_default_chunk_size_mb"); err != nil {
			return FileUploadInitResult{}, err
		} else if ok && v > 0 {
			in.ChunkSize = int(v * 1024 * 1024)
		}
	}
	if in.ChunkSize <= 0 {
		in.ChunkSize = 4 * 1024 * 1024
	}

	calculatedTotalChunks := int((in.TotalSize + int64(in.ChunkSize) - 1) / int64(in.ChunkSize))
	if in.TotalChunks <= 0 {
		in.TotalChunks = calculatedTotalChunks
	}
	if in.TotalChunks <= 0 {
		return FileUploadInitResult{}, fmt.Errorf("invalid total_chunks")
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
		task, chunkIndexes, err := s.getUploadTaskAndChunks(ctx, uid, in.UploadID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return FileUploadInitResult{}, ErrUploadTaskNotFound
			}
			return FileUploadInitResult{}, err
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
		UploadID:    uploadID,
		UserID:      &uidCopy,
		Filename:    in.Filename,
		TotalSize:   &totalSizeCopy,
		ChunkSize:   &chunkSizeCopy,
		TotalChunks: &totalChunksCopy,
		Status:      0,
	}
	if err := s.db.WithContext(ctx).Create(&task).Error; err != nil {
		return FileUploadInitResult{}, err
	}

	return FileUploadInitResult{UploadID: uploadID, UploadedChunks: []int{}, UploadedCount: 0, Status: 0}, nil
}

// ValidateUploadChunk 校验分片上传任务归属与状态。
func (s *FileService) ValidateUploadChunk(ctx context.Context, uid int64, uploadID string, chunkIndex int) (model.UploadTask, error) {
	var task model.UploadTask
	if err := s.db.WithContext(ctx).Where("upload_id = ?", uploadID).First(&task).Error; err != nil {
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
	if task.TotalChunks != nil && chunkIndex >= *task.TotalChunks {
		return model.UploadTask{}, ErrChunkIndexOutOfRange
	}
	return task, nil
}

// MarkChunkUploaded 记录分片上传完成并刷新已上传分片数。
func (s *FileService) MarkChunkUploaded(ctx context.Context, task model.UploadTask, uploadID string, chunkIndex int, chunkSize int64, chunkPath string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row model.UploadChunk
		err := tx.Where("upload_id = ? AND chunk_index = ?", uploadID, chunkIndex).First(&row).Error
		if err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			chunkSizeVal := chunkSize
			row = model.UploadChunk{
				UploadID:   uploadID,
				ChunkIndex: chunkIndex,
				ChunkSize:  &chunkSizeVal,
				ChunkPath:  chunkPath,
				Status:     1,
			}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		} else {
			chunkSizeVal := chunkSize
			if err := tx.Model(&row).Updates(map[string]interface{}{
				"chunk_size": chunkSizeVal,
				"chunk_path": chunkPath,
				"status":     1,
			}).Error; err != nil {
				return err
			}
		}

		var doneCount int64
		if err := tx.Model(&model.UploadChunk{}).Where("upload_id = ? AND status = 1", uploadID).Count(&doneCount).Error; err != nil {
			return err
		}
		return tx.Model(&model.UploadTask{}).Where("id = ?", task.ID).Update("uploaded_chunks", int(doneCount)).Error
	})
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

	var task model.UploadTask
	if err := s.db.WithContext(ctx).Where("upload_id = ?", uploadID).First(&task).Error; err != nil {
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
	if task.TotalChunks == nil || task.ChunkSize == nil || task.TotalSize == nil {
		return FileUploadTaskSnapshot{}, ErrUploadMetaIncomplete
	}

	var chunks []model.UploadChunk
	if err := s.db.WithContext(ctx).
		Where("upload_id = ? AND status = 1", uploadID).
		Order("chunk_index ASC").
		Find(&chunks).Error; err != nil {
		return FileUploadTaskSnapshot{}, err
	}
	if len(chunks) != *task.TotalChunks {
		return FileUploadTaskSnapshot{}, ErrChunksNotComplete
	}

	return FileUploadTaskSnapshot{Task: task, TotalSize: *task.TotalSize, ChunkSize: *task.ChunkSize, TotalChunk: *task.TotalChunks}, nil
}

// CompleteUpload 完成上传后写入 storage/file 及任务状态。
func (s *FileService) CompleteUpload(ctx context.Context, uid int64, parentID int64, uploadID string, snapshot FileUploadTaskSnapshot, storagePath string, mergedSize int64, fileHash string, clientIP string) (model.File, error) {
	var createdFile model.File
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		storageID, err := ensureStorageInService(tx, fileHash, storagePath, mergedSize)
		if err != nil {
			return err
		}

		finalName, err := ensureUniqueFilenameInService(tx, parentID, snapshot.Task.Filename)
		if err != nil {
			return err
		}

		fileType := strings.TrimPrefix(strings.ToLower(filepath.Ext(finalName)), ".")
		createdFile = model.File{
			UserID:    uid,
			Filename:  finalName,
			ParentID:  parentID,
			IsFolder:  0,
			FileType:  fileType,
			Size:      mergedSize,
			StorageID: &storageID,
		}
		if err := tx.Create(&createdFile).Error; err != nil {
			return err
		}

		if err := tx.Model(&model.UploadTask{}).Where("id = ?", snapshot.Task.ID).Updates(map[string]interface{}{
			"status":          1,
			"uploaded_chunks": snapshot.TotalChunk,
		}).Error; err != nil {
			return err
		}

		if err := tx.Where("upload_id = ?", uploadID).Delete(&model.UploadChunk{}).Error; err != nil {
			return err
		}

		return nil
	})
	if err != nil {
		return model.File{}, err
	}

	_ = s.logOperation(ctx, uid, &createdFile.ID, "upload_complete", "success", fmt.Sprintf("upload_id=%s", uploadID), clientIP)
	return createdFile, nil
}

// UploadProgress 查询上传进度。
func (s *FileService) UploadProgress(ctx context.Context, uid int64, uploadID string) (FileUploadProgressResult, error) {
	task, uploadedChunks, err := s.getUploadTaskAndChunks(ctx, uid, uploadID)
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

	return FileUploadProgressResult{
		UploadID:       task.UploadID,
		Status:         task.Status,
		UploadedCount:  len(uploadedChunks),
		TotalChunks:    totalChunks,
		UploadedChunks: uploadedChunks,
		MissingChunks:  missingChunks,
		Progress:       progress,
	}, nil
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

	var f model.File
	if err := s.db.WithContext(ctx).First(&f, "id = ? AND is_deleted = 0", fileID).Error; err != nil {
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

	var storage model.Storage
	if err := s.db.WithContext(ctx).First(&storage, "id = ?", *f.StorageID).Error; err != nil {
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

	if targetParentID > 0 {
		canWrite, err := s.canAccessFile(ctx, uid, targetParentID, true)
		if err != nil {
			return nil, err
		}
		if !canWrite {
			return nil, ErrFilePermissionDenied
		}

		var target model.File
		if err := s.db.WithContext(ctx).First(&target, "id = ? AND is_deleted = 0", targetParentID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, ErrTargetFolderNotFound
			}
			return nil, err
		}
		if target.IsFolder != 1 {
			return nil, ErrTargetNotFolder
		}
	}

	updated := make([]int64, 0, len(fileIDs))
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, fileID := range uniqueInt64InService(fileIDs) {
			if fileID <= 0 {
				return fmt.Errorf("invalid file id")
			}

			canWrite, err := s.canAccessFile(tx.Statement.Context, uid, fileID, true)
			if err != nil {
				return err
			}
			if !canWrite {
				return ErrFilePermissionDenied
			}

			var src model.File
			if err := tx.First(&src, "id = ? AND is_deleted = 0", fileID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrFileNotFound
				}
				return err
			}

			if src.ID == targetParentID {
				return ErrCannotMoveToItself
			}

			if src.IsFolder == 1 && targetParentID > 0 {
				isDescendant, err := isDescendantFolderInService(tx.Statement.Context, tx, targetParentID, src.ID)
				if err != nil {
					return err
				}
				if isDescendant {
					return ErrCannotMoveToDesc
				}
			}

			finalName, err := ensureUniqueFilenameInService(tx, targetParentID, src.Filename)
			if err != nil {
				return err
			}

			if err := tx.Model(&src).Updates(map[string]interface{}{"parent_id": targetParentID, "filename": finalName}).Error; err != nil {
				return err
			}
			updated = append(updated, src.ID)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	_ = s.logOperation(ctx, uid, nil, "move", "success", fmt.Sprintf("count=%d,target_parent=%d", len(updated), targetParentID), clientIP)
	return updated, nil
}

// CopyFiles 批量复制文件或文件夹。
func (s *FileService) CopyFiles(ctx context.Context, uid int64, fileIDs []int64, targetParentID int64, clientIP string) ([]int64, error) {
	if targetParentID < 0 {
		return nil, ErrFileInvalidParentID
	}

	if targetParentID > 0 {
		canWrite, err := s.canAccessFile(ctx, uid, targetParentID, true)
		if err != nil {
			return nil, err
		}
		if !canWrite {
			return nil, ErrFilePermissionDenied
		}
	}

	newIDs := make([]int64, 0)
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, fileID := range uniqueInt64InService(fileIDs) {
			if fileID <= 0 {
				return fmt.Errorf("invalid file id")
			}

			canRead, err := s.canAccessFile(tx.Statement.Context, uid, fileID, false)
			if err != nil {
				return err
			}
			if !canRead {
				return ErrFilePermissionDenied
			}

			var src model.File
			if err := tx.First(&src, "id = ? AND is_deleted = 0", fileID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrFileNotFound
				}
				return err
			}

			copiedID, err := copyNodeInService(tx, uid, src, targetParentID)
			if err != nil {
				return err
			}
			newIDs = append(newIDs, copiedID)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	_ = s.logOperation(ctx, uid, nil, "copy", "success", fmt.Sprintf("count=%d,target_parent=%d", len(newIDs), targetParentID), clientIP)
	return newIDs, nil
}

// ListRecycleBin 获取当前用户回收站列表。
func (s *FileService) ListRecycleBin(ctx context.Context, uid int64) ([]model.File, error) {
	var rows []model.File
	if err := s.db.WithContext(ctx).Where("user_id = ? AND is_deleted = 1", uid).Order("update_time DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// RestoreFromRecycleBin 恢复回收站条目。
func (s *FileService) RestoreFromRecycleBin(ctx context.Context, uid int64, fileID int64, clientIP string) (model.File, error) {
	var restored model.File
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&restored, "id = ? AND is_deleted = 1", fileID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrRecycleItemNotFound
			}
			return err
		}
		if restored.UserID != uid {
			return ErrFilePermissionDenied
		}

		finalName, err := ensureUniqueFilenameInService(tx, restored.ParentID, restored.Filename)
		if err != nil {
			return err
		}

		if err := tx.Model(&restored).Updates(map[string]interface{}{"is_deleted": 0, "filename": finalName}).Error; err != nil {
			return err
		}
		restored.Filename = finalName
		restored.IsDeleted = 0
		return nil
	})
	if err != nil {
		return model.File{}, err
	}

	_ = s.logOperation(ctx, uid, &restored.ID, "restore", "success", "restore from recycle bin", clientIP)
	return restored, nil
}

// PermanentDeleteFromRecycleBin 彻底删除回收站条目。
func (s *FileService) PermanentDeleteFromRecycleBin(ctx context.Context, uid int64, fileID int64, clientIP string) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var root model.File
		if err := tx.First(&root, "id = ? AND is_deleted = 1", fileID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrRecycleItemNotFound
			}
			return err
		}
		if root.UserID != uid {
			return ErrFilePermissionDenied
		}

		ids, err := collectDescendantIDsInService(tx.Statement.Context, tx, root.ID)
		if err != nil {
			return err
		}
		ids = append(ids, root.ID)

		if err := tx.Where("image_file_id IN ? OR video_file_id IN ?", ids, ids).Delete(&model.LivePhoto{}).Error; err != nil {
			return err
		}

		return tx.Where("id IN ?", ids).Delete(&model.File{}).Error
	})
	if err != nil {
		return err
	}

	_ = s.logOperation(ctx, uid, &fileID, "permanent_delete", "success", "delete from recycle bin", clientIP)
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
	if err := s.db.WithContext(ctx).Create(&share).Error; err != nil {
		return FileCreateShareResult{}, err
	}

	_ = s.logOperation(ctx, uid, &fileID, "create_share", "success", fmt.Sprintf("share_code=%s", shareCode), clientIP)
	return FileCreateShareResult{ShareCode: share.ShareCode, ExpireTime: share.ExpireTime}, nil
}

// ListShares 查询当前用户的分享记录。
func (s *FileService) ListShares(ctx context.Context, uid int64) ([]model.Share, error) {
	var shares []model.Share
	if err := s.db.WithContext(ctx).Where("user_id = ?", uid).Order("create_time DESC").Find(&shares).Error; err != nil {
		return nil, err
	}
	return shares, nil
}

// RevokeShare 撤销指定分享。
func (s *FileService) RevokeShare(ctx context.Context, uid int64, code string, clientIP string) error {
	res := s.db.WithContext(ctx).Where("share_code = ? AND user_id = ?", code, uid).Delete(&model.Share{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	_ = s.logOperation(ctx, uid, nil, "revoke_share", "success", fmt.Sprintf("share_code=%s", code), clientIP)
	return nil
}

// GetPublicShareInfo 获取公开分享的分享与文件信息。
func (s *FileService) GetPublicShareInfo(ctx context.Context, code string) (model.Share, model.File, error) {
	return s.resolveValidShare(ctx, code)
}

// ResolvePublicShareDownload 获取公开分享下载目标（文件或目录）。
func (s *FileService) ResolvePublicShareDownload(ctx context.Context, code string) (model.File, *model.Storage, error) {
	_, file, err := s.resolveValidShare(ctx, code)
	if err != nil {
		return model.File{}, nil, err
	}
	if file.IsFolder == 1 {
		return file, nil, nil
	}
	if file.StorageID == nil {
		return model.File{}, nil, ErrStorageNotFound
	}

	var storage model.Storage
	if err := s.db.WithContext(ctx).First(&storage, "id = ?", *file.StorageID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.File{}, nil, ErrStorageNotFound
		}
		return model.File{}, nil, err
	}
	if _, err := os.Stat(storage.FilePath); err != nil {
		return model.File{}, nil, ErrPhysicalFileNotFound
	}

	return file, &storage, nil
}

// CollectBatchDownloadEntries 收集批量下载的可下载条目（无权限或异常条目会被跳过）。
func (s *FileService) CollectBatchDownloadEntries(ctx context.Context, uid int64, fileIDs []int64) ([]FileBatchDownloadEntry, error) {
	if s.fileRepo != nil {
		rows, err := s.fileRepo.CollectBatchDownloadEntries(ctx, uid, fileIDs)
		if err != nil {
			return nil, err
		}
		entries := make([]FileBatchDownloadEntry, 0, len(rows))
		for _, row := range rows {
			entries = append(entries, FileBatchDownloadEntry{File: row.File, IsFolderZip: row.IsFolderZip, StoragePath: row.StoragePath})
		}
		return entries, nil
	}

	entries := make([]FileBatchDownloadEntry, 0)
	for _, fileID := range uniqueInt64InService(fileIDs) {
		if fileID <= 0 {
			continue
		}

		canRead, err := s.canAccessFile(ctx, uid, fileID, false)
		if err != nil || !canRead {
			continue
		}

		var f model.File
		if err := s.db.WithContext(ctx).First(&f, "id = ? AND is_deleted = 0", fileID).Error; err != nil {
			continue
		}

		if f.IsFolder == 1 {
			entries = append(entries, FileBatchDownloadEntry{File: f, IsFolderZip: true})
			continue
		}

		if f.StorageID == nil {
			continue
		}

		var storage model.Storage
		if err := s.db.WithContext(ctx).First(&storage, "id = ?", *f.StorageID).Error; err != nil {
			continue
		}
		if _, err := os.Stat(storage.FilePath); err != nil {
			continue
		}

		entries = append(entries, FileBatchDownloadEntry{File: f, StoragePath: storage.FilePath})
	}

	return entries, nil
}

// SearchFiles 按名称/类型/时间进行检索和筛选。
func (s *FileService) SearchFiles(ctx context.Context, uid int64, q FileSearchQuery) (FileSearchResult, error) {
	if s.fileRepo != nil {
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

	q.Name = strings.TrimSpace(q.Name)
	q.FileType = strings.ToLower(strings.TrimSpace(q.FileType))
	q.StartTime = strings.TrimSpace(q.StartTime)
	q.EndTime = strings.TrimSpace(q.EndTime)
	if q.Page <= 0 {
		q.Page = 1
	}
	if q.Limit <= 0 || q.Limit > 200 {
		q.Limit = 50
	}

	dbq := s.db.WithContext(ctx).
		Table("file f").
		Select("f.*").
		Joins("LEFT JOIN file_permission fp ON fp.file_id = f.id AND fp.user_id = ?", uid).
		Where("f.is_deleted = 0").
		Where("f.user_id = ? OR fp.permission IN ?", uid, []string{"read", "write"})

	if q.ParentID > 0 {
		dbq = dbq.Where("f.parent_id = ?", q.ParentID)
	}
	if q.Name != "" {
		dbq = dbq.Where("f.filename LIKE ?", "%"+q.Name+"%")
	}
	if q.FileType != "" {
		dbq = dbq.Where("LOWER(f.file_type) = ?", q.FileType)
	}
	if q.StartTime != "" {
		startAt, err := parseFlexibleTimeForFile(q.StartTime)
		if err != nil {
			return FileSearchResult{}, fmt.Errorf("invalid start_time")
		}
		dbq = dbq.Where("f.update_time >= ?", startAt)
	}
	if q.EndTime != "" {
		endAt, err := parseFlexibleTimeForFile(q.EndTime)
		if err != nil {
			return FileSearchResult{}, fmt.Errorf("invalid end_time")
		}
		dbq = dbq.Where("f.update_time <= ?", endAt)
	}

	var total int64
	if err := s.db.WithContext(ctx).Table("(?) as t", dbq).Count(&total).Error; err != nil {
		return FileSearchResult{}, err
	}

	offset := (q.Page - 1) * q.Limit
	var rows []model.File
	if err := dbq.Order("f.is_folder DESC, f.filename ASC").Limit(q.Limit).Offset(offset).Find(&rows).Error; err != nil {
		return FileSearchResult{}, err
	}

	return FileSearchResult{Items: rows, Total: total}, nil
}

// ListOperationLogs 查询操作日志。
func (s *FileService) ListOperationLogs(ctx context.Context, uid int64, isAdmin bool, q FileOperationLogQuery) (FileOperationLogListResult, error) {
	dbq := s.db.WithContext(ctx).Model(&model.OperationLog{})
	if !(isAdmin && q.All == 1) {
		dbq = dbq.Where("user_id = ?", uid)
	}
	if q.Action != "" {
		dbq = dbq.Where("action = ?", q.Action)
	}
	if q.Status != "" {
		dbq = dbq.Where("status = ?", q.Status)
	}
	if q.StartTime != "" {
		startAt, err := parseFlexibleTimeForFile(q.StartTime)
		if err != nil {
			return FileOperationLogListResult{}, fmt.Errorf("invalid start_time")
		}
		dbq = dbq.Where("create_time >= ?", startAt)
	}
	if q.EndTime != "" {
		endAt, err := parseFlexibleTimeForFile(q.EndTime)
		if err != nil {
			return FileOperationLogListResult{}, fmt.Errorf("invalid end_time")
		}
		dbq = dbq.Where("create_time <= ?", endAt)
	}

	var total int64
	if err := dbq.Count(&total).Error; err != nil {
		return FileOperationLogListResult{}, err
	}

	offset := (q.Page - 1) * q.Limit
	var rows []model.OperationLog
	if err := dbq.Order("id DESC").Limit(q.Limit).Offset(offset).Find(&rows).Error; err != nil {
		return FileOperationLogListResult{}, err
	}

	return FileOperationLogListResult{Items: rows, Total: total}, nil
}

// ExportOperationLogs 导出操作日志查询（返回行数据，由 handler 输出 CSV）。
func (s *FileService) ExportOperationLogs(ctx context.Context, uid int64, isAdmin bool, q FileOperationLogQuery) ([]model.OperationLog, error) {
	dbq := s.db.WithContext(ctx).Model(&model.OperationLog{})
	if !(isAdmin && q.All == 1) {
		dbq = dbq.Where("user_id = ?", uid)
	}
	if q.Action != "" {
		dbq = dbq.Where("action = ?", q.Action)
	}
	if q.Status != "" {
		dbq = dbq.Where("status = ?", q.Status)
	}
	if q.StartTime != "" {
		startAt, err := parseFlexibleTimeForFile(q.StartTime)
		if err != nil {
			return nil, fmt.Errorf("invalid start_time")
		}
		dbq = dbq.Where("create_time >= ?", startAt)
	}
	if q.EndTime != "" {
		endAt, err := parseFlexibleTimeForFile(q.EndTime)
		if err != nil {
			return nil, fmt.Errorf("invalid end_time")
		}
		dbq = dbq.Where("create_time <= ?", endAt)
	}

	var rows []model.OperationLog
	if err := dbq.Order("id DESC").Limit(q.Limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (s *FileService) canAccessFile(ctx context.Context, userID int64, fileID int64, requireWrite bool) (bool, error) {
	var f model.File
	err := s.db.WithContext(ctx).First(&f, "id = ? AND is_deleted = 0", fileID).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return false, nil
		}
		return false, err
	}
	if f.UserID == userID {
		return true, nil
	}

	permissions := []string{"read", "write"}
	if requireWrite {
		permissions = []string{"write"}
	}

	var count int64
	err = s.db.WithContext(ctx).
		Model(&model.FilePermission{}).
		Where("file_id = ? AND user_id = ? AND permission IN ?", fileID, userID, permissions).
		Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s *FileService) getSystemSettingInt64(ctx context.Context, key string) (int64, bool, error) {
	var row model.SystemSetting
	err := s.db.WithContext(ctx).Where("setting_key = ?", key).First(&row).Error
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

func (s *FileService) logOperation(ctx context.Context, userID int64, fileID *int64, action string, status string, detail string, clientIP string) error {
	if len(detail) > 500 {
		detail = detail[:500]
	}
	uid := userID
	row := model.OperationLog{UserID: &uid, FileID: fileID, Action: action, Status: status, Detail: detail, ClientIP: clientIP}
	return s.db.WithContext(ctx).Create(&row).Error
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

func getUploadTaskAndChunks(ctx context.Context, db *gorm.DB, userID int64, uploadID string) (model.UploadTask, []int, error) {
	var task model.UploadTask
	err := db.WithContext(ctx).Where("upload_id = ?", uploadID).First(&task).Error
	if err != nil {
		return model.UploadTask{}, nil, err
	}
	if task.UserID == nil || *task.UserID != userID {
		return model.UploadTask{}, nil, gorm.ErrRecordNotFound
	}

	var rows []model.UploadChunk
	err = db.WithContext(ctx).
		Where("upload_id = ? AND status = 1", uploadID).
		Order("chunk_index ASC").
		Find(&rows).Error
	if err != nil {
		return model.UploadTask{}, nil, err
	}

	chunkIndexes := make([]int, 0, len(rows))
	for _, row := range rows {
		chunkIndexes = append(chunkIndexes, row.ChunkIndex)
	}

	return task, chunkIndexes, nil
}

func (s *FileService) getUploadTaskAndChunks(ctx context.Context, userID int64, uploadID string) (model.UploadTask, []int, error) {
	return getUploadTaskAndChunks(ctx, s.db, userID, uploadID)
}

func ensureStorageInService(tx *gorm.DB, fileHash string, storagePath string, size int64) (int64, error) {
	var existing model.Storage
	err := tx.Where("file_hash = ?", fileHash).First(&existing).Error
	if err == nil {
		_ = os.Remove(storagePath)
		return existing.ID, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, err
	}

	sizeCopy := size
	storage := model.Storage{FileHash: fileHash, FilePath: storagePath, FileSize: &sizeCopy}
	if err := tx.Create(&storage).Error; err != nil {
		return 0, err
	}
	return storage.ID, nil
}

func ensureUniqueFilenameInService(tx *gorm.DB, parentID int64, baseName string) (string, error) {
	finalName := baseName
	for i := 0; i < 100; i++ {
		var count int64
		err := tx.Model(&model.File{}).
			Where("parent_id = ? AND filename = ? AND is_deleted = 0", parentID, finalName).
			Count(&count).Error
		if err != nil {
			return "", err
		}
		if count == 0 {
			return finalName, nil
		}
		finalName = appendNameSuffixInService(baseName, time.Now().Format("20060102150405"))
	}
	return "", fmt.Errorf("failed to allocate unique filename")
}

func appendNameSuffixInService(name string, suffix string) string {
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	if ext == "" {
		return fmt.Sprintf("%s_%s", base, suffix)
	}
	return fmt.Sprintf("%s_%s%s", base, suffix, ext)
}

func generateUploadID(userID int64) string {
	return fmt.Sprintf("u%d_%d", userID, time.Now().UnixNano())
}

func (s *FileService) resolveValidShare(ctx context.Context, code string) (model.Share, model.File, error) {
	if s.fileRepo != nil {
		share, file, err := s.fileRepo.ResolveValidShare(ctx, code)
		if err != nil {
			if errors.Is(err, contract.ErrShareExpired) {
				return model.Share{}, model.File{}, ErrShareExpired
			}
			return model.Share{}, model.File{}, err
		}
		return share, file, nil
	}

	code = strings.TrimSpace(code)
	if code == "" {
		return model.Share{}, model.File{}, gorm.ErrRecordNotFound
	}

	var share model.Share
	if err := s.db.WithContext(ctx).Where("share_code = ?", code).First(&share).Error; err != nil {
		return model.Share{}, model.File{}, err
	}
	if share.ExpireTime != nil && time.Now().After(*share.ExpireTime) {
		return model.Share{}, model.File{}, ErrShareExpired
	}
	if share.FileID == nil {
		return model.Share{}, model.File{}, gorm.ErrRecordNotFound
	}

	var file model.File
	if err := s.db.WithContext(ctx).First(&file, "id = ? AND is_deleted = 0", *share.FileID).Error; err != nil {
		return model.Share{}, model.File{}, err
	}

	return share, file, nil
}

func copyNodeInService(tx *gorm.DB, newOwnerID int64, src model.File, targetParentID int64) (int64, error) {
	finalName, err := ensureUniqueFilenameInService(tx, targetParentID, src.Filename)
	if err != nil {
		return 0, err
	}

	newNode := model.File{
		UserID:         newOwnerID,
		Filename:       finalName,
		ParentID:       targetParentID,
		IsFolder:       src.IsFolder,
		FileType:       src.FileType,
		Size:           src.Size,
		StorageID:      src.StorageID,
		WorkspaceID:    src.WorkspaceID,
		CurrentVersion: src.CurrentVersion,
		IsDeleted:      0,
	}
	if err := tx.Create(&newNode).Error; err != nil {
		return 0, err
	}

	if src.IsFolder == 1 {
		var children []model.File
		if err := tx.Where("parent_id = ? AND is_deleted = 0", src.ID).Order("id ASC").Find(&children).Error; err != nil {
			return 0, err
		}
		for _, child := range children {
			if _, err := copyNodeInService(tx, newOwnerID, child, newNode.ID); err != nil {
				return 0, err
			}
		}
	}

	return newNode.ID, nil
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
