package transfer

import (
	"errors"
	"os"
	"path/filepath"

	"cloudenest_go/internal/blob"
	"cloudenest_go/internal/service/contract"
	"cloudenest_go/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CheckVolume validates the registered volume before task/artifact locks.
// Ownership handoffs only decrease charged bytes; allocation alone locks policy.
func CheckVolume(tx *gorm.DB, token string) error {
	if !blob.ValidToken(token) {
		return contract.ErrTransferUnavailable
	}
	var artifact model.TransferArtifact
	if err := tx.First(&artifact, "token=?", token).Error; err != nil {
		return unavailable(err)
	}
	var volume model.TransferVolume
	if err := tx.First(&volume, "id=?", artifact.VolumeID).Error; err != nil {
		return unavailable(err)
	}
	return nil
}
func registered(tx *gorm.DB, token string, task model.UploadTask, kind, path string, size int64, digest string, active bool) (model.TransferArtifact, error) {
	var artifact model.TransferArtifact
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&artifact, "token=?", token).Error; err != nil {
		return artifact, unavailable(err)
	}
	var volume model.TransferVolume
	if err := tx.First(&volume, "id=?", artifact.VolumeID).Error; err != nil {
		return artifact, unavailable(err)
	}
	expectedState := "chunk"
	if active {
		expectedState = "ready"
	}
	if task.UserID == nil || task.TransferProtocol != 1 || task.TransferVolume != volume.ID || artifact.UserID != *task.UserID || artifact.TaskID != task.ID || artifact.Kind != kind || artifact.State != expectedState || artifact.Size != size || artifact.SHA256 != digest || filepath.Join(volume.RootPath, artifact.RelativePath) != path || !validRelative(kind, artifact.Token, artifact.RelativePath) {
		return artifact, contract.ErrTransferUnavailable
	}
	if active {
		if artifact.TransferEpoch != task.TransferEpoch {
			return artifact, contract.ErrUploadContract
		}
		var count int64
		if err := tx.Model(&model.TransferArtifact{}).Where("token=? AND expires_at>UTC_TIMESTAMP(6)", token).Count(&count).Error; err != nil {
			return artifact, err
		}
		if count != 1 {
			return artifact, contract.ErrUploadContract
		}
	}
	canonical, err := blob.CanonicalRoot(volume.RootPath)
	if err != nil || canonical != volume.RootPath {
		return artifact, contract.ErrTransferUnavailable
	}
	root, err := os.OpenRoot(canonical)
	if err != nil {
		return artifact, err
	}
	defer root.Close()
	if err := blob.RegularPath(root, artifact.RelativePath); err != nil {
		return artifact, err
	}
	file, err := root.Open(artifact.RelativePath)
	if err != nil {
		return artifact, err
	}
	defer file.Close()
	device, inode, err := fileIdentity(file)
	if err != nil || device != artifact.Device || inode != artifact.Inode {
		return artifact, contract.ErrStorageIntegrity
	}
	return artifact, nil
}
func ValidateChunk(tx *gorm.DB, token string, task model.UploadTask, path string, size int64, hash string, active bool) error {
	_, err := registered(tx, token, task, "chunk", path, size, hash, active)
	return unavailable(err)
}
func AttachChunk(tx *gorm.DB, token string, task model.UploadTask, path string, size int64, hash string) error {
	artifact, err := registered(tx, token, task, "chunk", path, size, hash, true)
	if err != nil {
		return unavailable(err)
	}
	if task.MergeArtifact != "" {
		return contract.ErrTransferBusy
	}
	if err := tx.Model(&artifact).Update("state", "chunk").Error; err != nil {
		return err
	}
	return record(tx, artifact.UserID, "transfer_chunk", token)
}
func Publish(tx *gorm.DB, token string, task model.UploadTask, path, canonical string, size int64, hash string) error {
	if task.MergeArtifact != token {
		return contract.ErrUploadContract
	}
	artifact, err := registered(tx, token, task, "merge", path, size, hash, true)
	if err != nil {
		return unavailable(err)
	}
	state := "published"
	if canonical != path {
		state = "discard"
	}
	if err := tx.Model(&artifact).Update("state", state).Error; err != nil {
		return err
	}
	if err := tx.Model(&model.UploadTask{}).Where("id=?", task.ID).Update("merge_artifact", "").Error; err != nil {
		return err
	}
	return record(tx, artifact.UserID, "transfer_"+state, token)
}
func isMissing(err error) bool { return errors.Is(err, os.ErrNotExist) }
