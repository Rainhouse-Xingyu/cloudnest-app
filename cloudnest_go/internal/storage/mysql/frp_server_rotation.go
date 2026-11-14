package mysql

import (
	"cloudenest_go/model"
	"context"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (r *FrpRepo) reencryptServers(ctx context.Context, dryRun bool, report FRPEncryptionReport) (FRPEncryptionReport, error) {
	var last int64
	for {
		var ids []int64
		if err := r.db.WithContext(ctx).Model(&model.FrpServerRevision{}).Where("id>?", last).Order("id").Limit(100).Pluck("id", &ids).Error; err != nil {
			return report, err
		}
		if len(ids) == 0 {
			return report, nil
		}
		for _, id := range ids {
			current, rewritten := false, false
			err := r.frpTransaction(ctx, func(tx *gorm.DB) error {
				var revision model.FrpServerRevision
				if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&revision, id).Error; err != nil {
					return err
				}
				var server model.FrpServer
				if err := tx.First(&server, revision.ServerID).Error; err != nil {
					return err
				}
				// Authenticate and validate every revision, even current-key dry runs.
				if _, err := r.openServer(tx, server, id); err != nil {
					return err
				}
				current = r.keys.IsCurrentServer(revision.Data)
				if current || dryRun {
					return nil
				}
				plain, err := r.keys.OpenServer(server.UserID, server.ID, id, revision.Data)
				if err != nil {
					return err
				}
				sealed, err := r.keys.SealServer(server.UserID, server.ID, id, plain)
				if err != nil {
					return err
				}
				if err := tx.Model(&revision).Update("data", sealed).Error; err != nil {
					return err
				}
				if err := tx.Create(&model.OperationLog{Action: "frp_server_encrypt", Detail: fmt.Sprintf("server_id=%d revision_id=%d", server.ID, id)}).Error; err != nil {
					return err
				}
				rewritten = true
				return nil
			})
			if err != nil {
				return report, fmt.Errorf("FRP server migration stopped at revision=%d: %w", id, err)
			}
			report.ServerScanned++
			if current {
				report.ServerCurrent++
			} else {
				report.ServerOldKey++
			}
			if rewritten {
				report.ServerRewritten++
			}
			last = id
		}
	}
}
