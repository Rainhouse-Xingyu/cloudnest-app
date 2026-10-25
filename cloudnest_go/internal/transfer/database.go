package transfer

import (
	"cloudenest_go/internal/blob"
	"cloudenest_go/internal/service/contract"
	"context"
	"gorm.io/gorm"
)

// Bind a local volume to one MySQL server/database, including maintenance DSNs.
// A copied database cannot silently reconcile another database's live root.
func DatabaseIdentity(ctx context.Context, db *gorm.DB) (string, error) {
	var identity string
	if err := db.WithContext(ctx).Raw("SELECT SHA2(CONCAT(@@server_uuid,CHAR(0),DATABASE()),256)").Scan(&identity).Error; err != nil {
		return "", err
	}
	if !blob.ValidToken(identity) {
		return "", contract.ErrTransferUnavailable
	}
	return identity, nil
}
