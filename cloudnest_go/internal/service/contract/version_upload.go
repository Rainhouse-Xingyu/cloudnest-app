package contract

import (
	"cloudenest_go/model"
	"strings"
	"unicode/utf8"
)

// Only the authenticated target-specific route sets this binding. The caller
// never supplies logical quota ownership or a storage object identifier.
type VersionUpload struct {
	FileID      int64
	BaseVersion int
	Comment     string
}

func (v VersionUpload) Validate() error {
	if v.FileID <= 0 || v.BaseVersion <= 0 || v.BaseVersion >= 2147483647 || !utf8.ValidString(v.Comment) || utf8.RuneCountInString(v.Comment) > 255 || strings.ContainsRune(v.Comment, '\x00') {
		return ErrInvalidInput
	}
	return nil
}

// Validate the kind/binding before selecting its ledger. Contract v1 stays
// strictly ordinary and old servers reject the new v2 task shape.
func UploadQuotaUser(t model.UploadTask) (int64, error) {
	if t.UserID == nil || *t.UserID <= 0 {
		return 0, ErrUploadContract
	}
	switch t.ContractVersion {
	case 1:
		if t.VersionFileID != nil || t.VersionBase != 0 || t.VersionComment != "" || t.QuotaUserID != nil {
			return 0, ErrUploadContract
		}
		return *t.UserID, nil
	case 2:
		if t.VersionFileID == nil || t.QuotaUserID == nil || *t.QuotaUserID <= 0 || t.ParentID != 0 {
			return 0, ErrUploadContract
		}
		v := VersionUpload{FileID: *t.VersionFileID, BaseVersion: t.VersionBase, Comment: t.VersionComment}
		if v.Validate() != nil {
			return 0, ErrUploadContract
		}
		return *t.QuotaUserID, nil
	case 3:
		// A terminal request-key cancellation has no file binding, quota, or
		// publication receipt. It fences initialization even before allocation.
		if t.Status != 3 || t.ReservedBytes != 0 || t.VersionFileID != nil || t.QuotaUserID != nil || t.VersionBase != 0 || t.VersionComment != "" || t.ParentID != 0 || t.ResultVersion != 0 || t.ResultFileID != nil || t.ResultJSON != nil {
			return 0, ErrUploadContract
		}
		return *t.UserID, nil
	default:
		return 0, ErrUploadContract
	}
}

func UploadVersionBinding(t model.UploadTask) *VersionUpload {
	if t.ContractVersion != 2 || t.VersionFileID == nil {
		return nil
	}
	return &VersionUpload{FileID: *t.VersionFileID, BaseVersion: t.VersionBase, Comment: t.VersionComment}
}
