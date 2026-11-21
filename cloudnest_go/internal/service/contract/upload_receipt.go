package contract

import (
	"encoding/json"
	"io"
	"strings"

	"cloudenest_go/model"
)

// UploadReceipt is an acknowledgement of an earlier commit, not a current file
// lookup or a storage capability. It deliberately has no FK retaining content.
// Never reconstruct a missing receipt by filename, timestamp, or hash.
func UploadReceipt(task model.UploadTask, uid, parentID int64) (model.File, error) {
	if task.UserID == nil || *task.UserID != uid {
		return model.File{}, ErrForbidden
	}
	owner, err := UploadQuotaUser(task)
	if err != nil {
		return model.File{}, err
	}
	if task.Status != 1 || task.ParentID != parentID {
		return model.File{}, ErrUploadContract
	}
	if task.ResultVersion == 0 && task.ResultFileID == nil && task.ResultJSON == nil {
		return model.File{}, ErrUploadReceiptMissing // a completed pre-receipt task
	}
	if task.ResultVersion != task.ContractVersion || task.ResultFileID == nil || task.ResultJSON == nil || len(*task.ResultJSON) > 8192 || task.ReservedBytes != 0 {
		return model.File{}, ErrUploadReceipt
	}
	var f model.File
	decoder := json.NewDecoder(strings.NewReader(*task.ResultJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&f); err != nil {
		return model.File{}, ErrUploadReceipt
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF || f.ID <= 0 || f.ID != *task.ResultFileID || f.UserID != owner || f.IsFolder != 0 || f.IsDeleted != 0 || f.StorageID == nil || *f.StorageID <= 0 || task.TotalSize == nil || f.Size != *task.TotalSize || f.Size < 0 || (f.Size == 0 && !IsEmptyUpload(task)) || ValidateFileName(f.Filename) != nil || f.CreateTime.IsZero() || f.UpdateTime.IsZero() {
		return model.File{}, ErrUploadReceipt
	}
	if task.ContractVersion == 1 && (f.ParentID != parentID || f.CurrentVersion != 1) {
		return model.File{}, ErrUploadReceipt
	}
	if task.ContractVersion == 2 && (f.ID != *task.VersionFileID || f.CurrentVersion != task.VersionBase+1) {
		return model.File{}, ErrUploadReceipt
	}
	return f, nil
}
