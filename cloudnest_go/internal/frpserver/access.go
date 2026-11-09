// Package frpserver provides shared authorization for immutable server revisions.
package frpserver

import (
	"cloudenest_go/internal/service/contract"
	"cloudenest_go/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CheckAccess runs inside a transaction after locking the device owner.
// Disabling a server prevents new rollback/deploy work, not already issued work.
func CheckAccess(tx *gorm.DB, owner, serverID, revisionID int64) error {
	if serverID <= 0 || revisionID <= 0 {
		return contract.ErrInvalidInput
	}
	var server model.FrpServer
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&server, serverID).Error; err != nil {
		return err
	}
	if server.UserID != owner {
		return contract.ErrForbidden
	}
	if !server.Enabled {
		return contract.ErrConflict
	}
	var revision model.FrpServerRevision
	return tx.Select("id").First(&revision, "id=? AND server_id=?", revisionID, serverID).Error
}
