package contract

import (
	"context"

	"cloudenest_go/model"
)

// LivePhotoBindPair 定义 Live Photo 绑定对。
type LivePhotoBindPair struct {
	ImageFileID int64
	VideoFileID int64
}

// LivePhotoRepository 定义 Live Photo 模块存储访问能力。
type LivePhotoRepository interface {
	GetActiveFileByID(ctx context.Context, fileID int64) (model.File, error)
	CountFilePermissionsByLevels(ctx context.Context, fileID int64, userID int64, permissions []string) (int64, error)
	CreateBindingWithLog(ctx context.Context, uid int64, imageFileID int64, videoFileID int64, clientIP string) (model.LivePhoto, error)
	GetLivePhotoByID(ctx context.Context, livePhotoID int64) (model.LivePhoto, error)
	DeleteBindingWithLog(ctx context.Context, uid int64, row model.LivePhoto, clientIP string) error
	ListAccessibleLivePhotos(ctx context.Context, uid int64, parentID int64, page int, limit int) ([]model.LivePhoto, int64, error)
	ListActiveFilesByIDs(ctx context.Context, fileIDs []int64) ([]model.File, error)
	GetLatestLivePhotoByFileID(ctx context.Context, fileID int64) (model.LivePhoto, error)
	ListWritableFilesByParent(ctx context.Context, userID int64, parentID int64) ([]model.File, error)
	ListLivePhotosByFileIDs(ctx context.Context, fileIDs []int64) ([]model.LivePhoto, error)
	CreateAutoBindPairsWithLog(ctx context.Context, uid int64, pairs []LivePhotoBindPair, clientIP string) ([]model.LivePhoto, []LivePhotoBindPair, error)
}
