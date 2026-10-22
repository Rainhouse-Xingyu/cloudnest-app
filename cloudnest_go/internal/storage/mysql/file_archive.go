package mysql

import (
	"context"

	"cloudenest_go/internal/service/contract"
	"cloudenest_go/model"
	"gorm.io/gorm"
)

func archiveStoragePath(tx *gorm.DB, file model.File) (string, error) {
	if file.StorageID == nil {
		return "", gorm.ErrRecordNotFound
	}
	var storage model.Storage
	if err := tx.First(&storage, "id = ?", *file.StorageID).Error; err != nil {
		return "", err
	}
	return storage.FilePath, nil
}

func archiveTreeEntries(tx *gorm.DB, uid, rootID int64) ([]contract.FolderZipEntry, error) {
	nodes, err := collectFileTree(tx, []int64{rootID}, 0, false, false)
	if err != nil {
		return nil, err
	}
	if nodes[0].File.IsFolder != 1 {
		return nil, contract.ErrTargetNotFolder
	}
	if err := requireTreeRead(tx, uid, nodes); err != nil {
		return nil, err
	}
	paths := make(map[int64]string, len(nodes))
	used := make(map[string]bool, len(nodes))
	entries := make([]contract.FolderZipEntry, 0, len(nodes))
	for _, node := range nodes {
		f := node.File
		if err := contract.ValidateFileName(f.Filename); err != nil {
			return nil, err
		}
		name := f.Filename
		if f.ID != rootID {
			name = paths[f.ParentID] + "/" + name
		}
		// ZIP header filenames use a uint16 byte length; leave room for directory '/'.
		if len(name) > 65534 || used[name] {
			return nil, contract.ErrConflict
		}
		paths[f.ID] = name
		used[name] = true
		entry := contract.FolderZipEntry{RelPath: name, IsDir: f.IsFolder == 1}
		if !entry.IsDir {
			if f.StorageID == nil {
				return nil, gorm.ErrRecordNotFound
			}
			entry.StorageID = *f.StorageID
			entry.StoragePath, err = archiveStoragePath(tx, f)
			if err != nil {
				return nil, err
			}
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func (r *FileRepo) CollectFolderZipEntries(ctx context.Context, uid, rootID int64) ([]contract.FolderZipEntry, error) {
	var entries []contract.FolderZipEntry
	err := fileReadSnapshot(ctx, r.db, func(tx *gorm.DB) error {
		var err error
		entries, err = archiveTreeEntries(tx, uid, rootID)
		return err
	})
	return entries, err
}

func (r *FileRepo) CollectBatchDownloadEntries(ctx context.Context, uid int64, fileIDs []int64) ([]contract.BatchDownloadEntry, error) {
	ids := uniqueInt64ForFileRepo(fileIDs)
	if len(ids) == 0 || len(ids) > maxFileTreeNodes {
		return nil, contract.ErrInvalidInput
	}
	entries := make([]contract.BatchDownloadEntry, 0, len(ids))
	err := fileReadSnapshot(ctx, r.db, func(tx *gorm.DB) error {
		remaining := maxFileTreeNodes
		for _, id := range ids {
			if id <= 0 {
				return contract.ErrInvalidInput
			}
			var f model.File
			if err := tx.First(&f, "id = ? AND is_deleted = 0", id).Error; err != nil {
				return err
			}
			if err := requireTreeRead(tx, uid, []fileTreeNode{{File: f}}); err != nil {
				return err
			}
			if err := contract.ValidateFileName(f.Filename); err != nil {
				return err
			}
			entry := contract.BatchDownloadEntry{File: f, IsFolderZip: f.IsFolder == 1}
			var err error
			if entry.IsFolderZip {
				entry.FolderEntries, err = archiveTreeEntries(tx, uid, id)
				remaining -= len(entry.FolderEntries)
			} else {
				entry.StoragePath, err = archiveStoragePath(tx, f)
				remaining--
			}
			if err != nil {
				return err
			}
			if remaining < 0 {
				return contract.ErrConflict
			}
			entries = append(entries, entry)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return entries, nil
}
