package service

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"cloudenest_go/internal/service/contract"
	"cloudenest_go/model"

	"gorm.io/gorm"
)

// LivePhotoService 承接 Live Photo 模块业务逻辑。
type LivePhotoService struct {
	repo contract.LivePhotoRepository
}

func NewLivePhotoService(repo contract.LivePhotoRepository) *LivePhotoService {
	return &LivePhotoService{repo: repo}
}

// LivePhotoBindResult 表示绑定后的结果。
type LivePhotoBindResult struct {
	Row       model.LivePhoto
	ImageFile model.File
	VideoFile model.File
}

// LivePhotoDetailResult 表示详情结果。
type LivePhotoDetailResult struct {
	Row       model.LivePhoto
	ImageFile model.File
	VideoFile model.File
}

// LivePhotoListItem 表示列表项。
type LivePhotoListItem struct {
	Row       model.LivePhoto
	ImageFile model.File
	VideoFile model.File
}

// LivePhotoListResult 表示列表查询结果。
type LivePhotoListResult struct {
	Items []LivePhotoListItem
	Total int64
}

// LivePhotoFileLookupResult 表示按文件反查结果。
type LivePhotoFileLookupResult struct {
	Row       model.LivePhoto
	FileRole  string
	ImageFile model.File
	VideoFile model.File
}

// LivePhotoAutoBindInput 表示自动绑定输入。
type LivePhotoAutoBindInput struct {
	ParentID *int64
	FileIDs  []int64
}

// LivePhotoAutoBindResult 表示自动绑定结果。
type LivePhotoAutoBindResult struct {
	TotalCandidates int
	CreatedItems    []map[string]interface{}
	SkippedItems    []map[string]interface{}
}

// LivePhotoListInput 表示列表查询输入。
type LivePhotoListInput struct {
	ParentID int64
	Page     int
	Limit    int
}

var (
	// ErrLivePhotoPermissionDenied 表示无权限访问。
	ErrLivePhotoPermissionDenied = errors.New("live photo permission denied")
)

// Bind 手动绑定图片和视频为一个 Live Photo。
func (s *LivePhotoService) Bind(ctx context.Context, uid int64, imageFileID int64, videoFileID int64, clientIP string) (LivePhotoBindResult, error) {
	canWriteImage, err := s.canAccessFile(ctx, uid, imageFileID, true)
	if err != nil {
		return LivePhotoBindResult{}, err
	}
	canWriteVideo, err := s.canAccessFile(ctx, uid, videoFileID, true)
	if err != nil {
		return LivePhotoBindResult{}, err
	}
	if !canWriteImage || !canWriteVideo {
		return LivePhotoBindResult{}, ErrLivePhotoPermissionDenied
	}

	imageFile, videoFile, err := s.loadLivePhotoFiles(ctx, imageFileID, videoFileID)
	if err != nil {
		return LivePhotoBindResult{}, err
	}

	created, err := s.repo.CreateBindingWithLog(ctx, uid, imageFileID, videoFileID, clientIP)
	if err != nil {
		return LivePhotoBindResult{}, err
	}

	return LivePhotoBindResult{Row: created, ImageFile: imageFile, VideoFile: videoFile}, nil
}

// Unbind 删除 Live Photo 绑定关系，不删除原始文件。
func (s *LivePhotoService) Unbind(ctx context.Context, uid int64, livePhotoID int64, clientIP string) error {
	row, err := s.repo.GetLivePhotoByID(ctx, livePhotoID)
	if err != nil {
		return err
	}

	canWriteImage, err := s.canAccessFile(ctx, uid, row.ImageFileID, true)
	if err != nil {
		return err
	}
	canWriteVideo, err := s.canAccessFile(ctx, uid, row.VideoFileID, true)
	if err != nil {
		return err
	}
	if !canWriteImage || !canWriteVideo {
		return ErrLivePhotoPermissionDenied
	}

	return s.repo.DeleteBindingWithLog(ctx, uid, row, clientIP)
}

// Detail 查询单个 Live Photo 的详情。
func (s *LivePhotoService) Detail(ctx context.Context, uid int64, livePhotoID int64) (LivePhotoDetailResult, error) {
	row, err := s.repo.GetLivePhotoByID(ctx, livePhotoID)
	if err != nil {
		return LivePhotoDetailResult{}, err
	}

	canReadImage, err := s.canAccessFile(ctx, uid, row.ImageFileID, false)
	if err != nil {
		return LivePhotoDetailResult{}, err
	}
	canReadVideo, err := s.canAccessFile(ctx, uid, row.VideoFileID, false)
	if err != nil {
		return LivePhotoDetailResult{}, err
	}
	if !canReadImage || !canReadVideo {
		return LivePhotoDetailResult{}, ErrLivePhotoPermissionDenied
	}

	imageFile, err := s.repo.GetActiveFileByID(ctx, row.ImageFileID)
	if err != nil {
		return LivePhotoDetailResult{}, err
	}
	videoFile, err := s.repo.GetActiveFileByID(ctx, row.VideoFileID)
	if err != nil {
		return LivePhotoDetailResult{}, err
	}

	return LivePhotoDetailResult{Row: row, ImageFile: imageFile, VideoFile: videoFile}, nil
}

// List 分页查询 Live Photo 列表，支持按目录过滤。
func (s *LivePhotoService) List(ctx context.Context, uid int64, q LivePhotoListInput) (LivePhotoListResult, error) {
	rows, total, err := s.repo.ListAccessibleLivePhotos(ctx, uid, q.ParentID, q.Page, q.Limit)
	if err != nil {
		return LivePhotoListResult{}, err
	}

	fileIDs := make([]int64, 0, len(rows)*2)
	for _, row := range rows {
		fileIDs = append(fileIDs, row.ImageFileID, row.VideoFileID)
	}

	fileMap := map[int64]model.File{}
	if len(fileIDs) > 0 {
		files, err := s.repo.ListActiveFilesByIDs(ctx, uniqueInt64(fileIDs))
		if err != nil {
			return LivePhotoListResult{}, err
		}
		for _, f := range files {
			fileMap[f.ID] = f
		}
	}

	items := make([]LivePhotoListItem, 0, len(rows))
	for _, row := range rows {
		imageFile, okImage := fileMap[row.ImageFileID]
		videoFile, okVideo := fileMap[row.VideoFileID]
		if !okImage || !okVideo {
			continue
		}
		items = append(items, LivePhotoListItem{Row: row, ImageFile: imageFile, VideoFile: videoFile})
	}

	return LivePhotoListResult{Items: items, Total: total}, nil
}

// GetByFileID 根据文件 ID 反查其所属的 Live Photo。
func (s *LivePhotoService) GetByFileID(ctx context.Context, uid int64, fileID int64) (*LivePhotoFileLookupResult, error) {
	canRead, err := s.canAccessFile(ctx, uid, fileID, false)
	if err != nil {
		return nil, err
	}
	if !canRead {
		return nil, ErrLivePhotoPermissionDenied
	}

	row, err := s.repo.GetLatestLivePhotoByFileID(ctx, fileID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}

	otherID := row.ImageFileID
	role := "video"
	if row.ImageFileID == fileID {
		otherID = row.VideoFileID
		role = "image"
	}
	canReadOther, err := s.canAccessFile(ctx, uid, otherID, false)
	if err != nil {
		return nil, err
	}
	if !canReadOther {
		return nil, ErrLivePhotoPermissionDenied
	}

	imageFile, err := s.repo.GetActiveFileByID(ctx, row.ImageFileID)
	if err != nil {
		return nil, err
	}
	videoFile, err := s.repo.GetActiveFileByID(ctx, row.VideoFileID)
	if err != nil {
		return nil, err
	}

	return &LivePhotoFileLookupResult{Row: row, FileRole: role, ImageFile: imageFile, VideoFile: videoFile}, nil
}

// AutoBind 按同目录+同名主干规则自动识别并绑定 Live Photo。
func (s *LivePhotoService) AutoBind(ctx context.Context, uid int64, req LivePhotoAutoBindInput, clientIP string) (LivePhotoAutoBindResult, error) {
	candidates, err := s.loadAutoBindCandidates(ctx, uid, req)
	if err != nil {
		return LivePhotoAutoBindResult{}, err
	}
	if len(candidates) == 0 {
		return LivePhotoAutoBindResult{TotalCandidates: 0, CreatedItems: []map[string]interface{}{}, SkippedItems: []map[string]interface{}{}}, nil
	}

	type pairGroup struct {
		images []model.File
		videos []model.File
	}
	groups := make(map[string]*pairGroup)
	for _, f := range candidates {
		ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(f.Filename), "."))
		key := buildAutoBindKey(f.ParentID, f.Filename)
		if key == "" {
			continue
		}
		if groups[key] == nil {
			groups[key] = &pairGroup{images: []model.File{}, videos: []model.File{}}
		}
		if isLivePhotoImageExt(ext) {
			groups[key].images = append(groups[key].images, f)
		} else if isLivePhotoVideoExt(ext) {
			groups[key].videos = append(groups[key].videos, f)
		}
	}

	candidateIDs := make([]int64, 0, len(candidates))
	for _, f := range candidates {
		candidateIDs = append(candidateIDs, f.ID)
	}

	boundImage := make(map[int64]bool)
	boundVideo := make(map[int64]bool)
	if len(candidateIDs) > 0 {
		existed, err := s.repo.ListLivePhotosByFileIDs(ctx, candidateIDs)
		if err != nil {
			return LivePhotoAutoBindResult{}, err
		}
		for _, row := range existed {
			boundImage[row.ImageFileID] = true
			boundVideo[row.VideoFileID] = true
		}
	}

	persistPairs := make([]contract.LivePhotoBindPair, 0)
	skippedItems := make([]map[string]interface{}, 0)
	for key, g := range groups {
		if len(g.images) == 0 || len(g.videos) == 0 {
			skippedItems = append(skippedItems, map[string]interface{}{"key": key, "reason": "pair not found"})
			continue
		}

		pairCount := len(g.images)
		if len(g.videos) < pairCount {
			pairCount = len(g.videos)
		}

		for i := 0; i < pairCount; i++ {
			img := g.images[i]
			vid := g.videos[i]

			if boundImage[img.ID] || boundVideo[vid.ID] {
				skippedItems = append(skippedItems, map[string]interface{}{"image_file_id": img.ID, "video_file_id": vid.ID, "reason": "already bound"})
				continue
			}

			boundImage[img.ID] = true
			boundVideo[vid.ID] = true
			persistPairs = append(persistPairs, contract.LivePhotoBindPair{ImageFileID: img.ID, VideoFileID: vid.ID})
		}
	}

	createdRows, duplicatePairs, err := s.repo.CreateAutoBindPairsWithLog(ctx, uid, persistPairs, clientIP)
	if err != nil {
		return LivePhotoAutoBindResult{}, err
	}

	createdItems := make([]map[string]interface{}, 0, len(createdRows))
	for _, row := range createdRows {
		createdItems = append(createdItems, map[string]interface{}{"id": row.ID, "image_file_id": row.ImageFileID, "video_file_id": row.VideoFileID, "create_time": row.CreateTime})
	}
	for _, pair := range duplicatePairs {
		skippedItems = append(skippedItems, map[string]interface{}{"image_file_id": pair.ImageFileID, "video_file_id": pair.VideoFileID, "reason": "already bound"})
	}

	return LivePhotoAutoBindResult{TotalCandidates: len(candidates), CreatedItems: createdItems, SkippedItems: skippedItems}, nil
}

// canAccessFile 复用 file + file_permission 的权限模型判断文件访问权限。
func (s *LivePhotoService) canAccessFile(ctx context.Context, userID int64, fileID int64, requireWrite bool) (bool, error) {
	f, err := s.repo.GetActiveFileByID(ctx, fileID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
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

	count, err := s.repo.CountFilePermissionsByLevels(ctx, fileID, userID, permissions)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// loadLivePhotoFiles 校验文件类型并返回图片文件和视频文件。
func (s *LivePhotoService) loadLivePhotoFiles(ctx context.Context, imageFileID int64, videoFileID int64) (model.File, model.File, error) {
	imageFile, err := s.repo.GetActiveFileByID(ctx, imageFileID)
	if err != nil {
		return model.File{}, model.File{}, err
	}
	videoFile, err := s.repo.GetActiveFileByID(ctx, videoFileID)
	if err != nil {
		return model.File{}, model.File{}, err
	}

	if imageFile.IsFolder == 1 || videoFile.IsFolder == 1 {
		return model.File{}, model.File{}, fmt.Errorf("folder cannot be bound")
	}

	imageExt := strings.ToLower(strings.TrimPrefix(filepath.Ext(imageFile.Filename), "."))
	videoExt := strings.ToLower(strings.TrimPrefix(filepath.Ext(videoFile.Filename), "."))
	if !isLivePhotoImageExt(imageExt) {
		return model.File{}, model.File{}, fmt.Errorf("image file type not supported")
	}
	if !isLivePhotoVideoExt(videoExt) {
		return model.File{}, model.File{}, fmt.Errorf("video file type not supported")
	}

	return imageFile, videoFile, nil
}

// loadAutoBindCandidates 根据请求加载自动绑定候选文件。
func (s *LivePhotoService) loadAutoBindCandidates(ctx context.Context, userID int64, req LivePhotoAutoBindInput) ([]model.File, error) {
	if len(req.FileIDs) > 0 {
		ids := uniqueInt64(req.FileIDs)
		for _, id := range ids {
			if id <= 0 {
				return nil, fmt.Errorf("invalid file id")
			}
			canWrite, err := s.canAccessFile(ctx, userID, id, true)
			if err != nil {
				return nil, err
			}
			if !canWrite {
				return nil, fmt.Errorf("permission denied")
			}
		}

		rows, err := s.repo.ListActiveFilesByIDs(ctx, ids)
		if err != nil {
			return nil, err
		}
		return rows, nil
	}

	if req.ParentID != nil {
		if *req.ParentID < 0 {
			return nil, fmt.Errorf("invalid parent_id")
		}
		if *req.ParentID > 0 {
			canWrite, err := s.canAccessFile(ctx, userID, *req.ParentID, true)
			if err != nil {
				return nil, err
			}
			if !canWrite {
				return nil, fmt.Errorf("permission denied")
			}
		}

		rows, err := s.repo.ListWritableFilesByParent(ctx, userID, *req.ParentID)
		if err != nil {
			return nil, err
		}
		return rows, nil
	}

	return []model.File{}, nil
}

func isLivePhotoImageExt(ext string) bool {
	allowed := map[string]bool{"jpg": true, "jpeg": true, "heic": true, "heif": true}
	return allowed[ext]
}

func isLivePhotoVideoExt(ext string) bool {
	allowed := map[string]bool{"mov": true}
	return allowed[ext]
}

func buildAutoBindKey(parentID int64, filename string) string {
	base := strings.TrimSpace(strings.TrimSuffix(filename, filepath.Ext(filename)))
	if base == "" {
		return ""
	}
	return fmt.Sprintf("%d::%s", parentID, strings.ToLower(base))
}

func uniqueInt64(input []int64) []int64 {
	set := make(map[int64]struct{}, len(input))
	out := make([]int64, 0, len(input))
	for _, v := range input {
		if _, ok := set[v]; ok {
			continue
		}
		set[v] = struct{}{}
		out = append(out, v)
	}
	return out
}
