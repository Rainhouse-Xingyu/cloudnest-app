package mysql

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"cloudenest_go/internal/service/contract"
	"cloudenest_go/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Read-only ownership is retained even when a device has been retired or moved.
// The historical resource claim still belongs to its original issuing owner.
func (r *FrpRepo) ListReservations(ctx context.Context, actor contract.FrpActor, device, after int64, limit int) ([]model.FrpReservation, error) {
	if actor.UserID <= 0 || device < 0 || after < 0 || limit < 1 || limit > 100 {
		return nil, contract.ErrInvalidInput
	}
	q := r.db.WithContext(ctx).Where("id>?", after)
	if !actor.Admin {
		q = q.Where("owner_id=?", actor.UserID)
	}
	if device > 0 {
		q = q.Where("device_id=?", device)
	}
	rows := make([]model.FrpReservation, 0)
	err := q.Order("id").Limit(limit).Find(&rows).Error
	return rows, err
}

// FRPReservationReview is safe to print in the maintenance CLI. The embedded
// claim omits the credential generation from JSON.
type FRPReservationReview struct {
	Claim       model.FrpReservation `json:"claim"`
	Eligibility string               `json:"eligibility"`
}

var evidenceReference = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,79}$`)

func ValidFRPReservationEvidence(ref string) bool { return evidenceReference.MatchString(ref) }

// ReconcileReservation checks one historical claim. With evidence it records
// the operator's external decommissioning confirmation and marks the claim
// inactive in the same transaction as its audit. Empty evidence only inspects.
func (r *FrpRepo) ReconcileReservation(ctx context.Context, id int64, evidence string) (FRPReservationReview, error) {
	var review FRPReservationReview
	if id <= 0 || (evidence != "" && !ValidFRPReservationEvidence(evidence)) {
		return review, contract.ErrInvalidInput
	}
	err := r.frpTransaction(ctx, func(tx *gorm.DB) error {
		claim := &review.Claim
		if err := tx.First(claim, "id=?", id).Error; err != nil {
			return err
		}
		if claim.ReconciledAt != nil {
			review.Eligibility = "already_reconciled"
			return nil
		}
		eligibility, err := historicalReservationEligibility(tx, *claim)
		if err != nil {
			return err
		}
		review.Eligibility = eligibility
		if evidence == "" {
			return nil
		}
		if eligibility != "historical" {
			return contract.ErrConflict
		}
		if err := tx.Model(claim).UpdateColumn("reconciled_at", gorm.Expr("UTC_TIMESTAMP(6)")).Error; err != nil {
			return err
		}
		if err := tx.Create(&model.OperationLog{Action: "frp.reservation.reconcile", Detail: fmt.Sprintf("reservation_id=%d device_id=%d config_id=%d evidence=%s", claim.ID, claim.DeviceID, claim.ConfigID, evidence)}).Error; err != nil {
			return err
		}
		if err := tx.First(claim, "id=?", id).Error; err != nil {
			return err
		}
		review.Eligibility = "reconciled"
		return nil
	})
	if err != nil {
		return FRPReservationReview{}, err
	}
	return review, nil
}

func historicalReservationEligibility(tx *gorm.DB, claim model.FrpReservation) (string, error) {
	if claim.CredentialID == "" && (claim.Kind != "unknown" || claim.ServerID != 0 || claim.Reference != fmt.Sprintf("legacy-config:%d", claim.ConfigID)) {
		return "unrecognized_generation", nil
	}
	if claim.DeviceID == 0 {
		return "historical", nil
	}
	// The device row serializes first enrollment and legacy JWT command pulls.
	// As in Agent authentication, lock it before reading the current binding.
	var device model.Device
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&device, "id=?", claim.DeviceID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "historical", nil
	}
	if err != nil {
		return "", err
	}
	var binding model.AgentBinding
	err = tx.Select("credential_id").Where("device_id=?", device.ID).Take(&binding).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "legacy_execution_enabled", nil
	}
	if err != nil {
		return "", err
	}
	if binding.CredentialID != nil && *binding.CredentialID == claim.CredentialID {
		return "current_generation", nil
	}
	return "historical", nil
}
