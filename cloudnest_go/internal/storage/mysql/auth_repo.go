package mysql

import (
	"context"
	"errors"

	"cloudenest_go/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// AuthRepo 是认证模块仓储实现。
type AuthRepo struct {
	db *gorm.DB
}

func NewAuthRepo(db *gorm.DB) *AuthRepo {
	return &AuthRepo{db: db}
}

func (r *AuthRepo) GetUserByUsername(ctx context.Context, username string) (model.User, error) {
	var u model.User
	if err := r.db.WithContext(ctx).Where("username = ?", username).First(&u).Error; err != nil {
		return model.User{}, err
	}
	return u, nil
}

func (r *AuthRepo) CheckUserIsAdmin(ctx context.Context, userID int64) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Table("user_role ur").
		Joins("JOIN role r ON ur.role_id = r.id").
		Where("ur.user_id = ? AND r.role_name = ?", userID, "admin").
		Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func (r *AuthRepo) GetUserProfile(ctx context.Context, userID int64) (model.User, error) {
	var u model.User
	err := r.db.WithContext(ctx).
		Select("id", "username", "email", "status", "auth_version", "create_time", "update_time").
		First(&u, userID).Error
	if err != nil {
		return model.User{}, err
	}
	return u, nil
}

func (r *AuthRepo) RevokeUserSessions(ctx context.Context, userID int64, clientIP string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.User{}).Where("id = ? AND status <> 0", userID).Update("auth_version", gorm.Expr("auth_version + 1"))
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		return tx.Create(&model.OperationLog{UserID: &userID, Action: "auth_logout_all", Status: "success", Detail: "all user sessions revoked", ClientIP: clientIP}).Error
	})
}

func (r *AuthRepo) ResetUserPassword(ctx context.Context, userID int64, expectedUsername, passwordHash string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var user model.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&user, userID).Error; err != nil {
			return err
		}
		// Compare in Go: MySQL username collation may ignore case or trailing spaces.
		if user.Username != expectedUsername {
			return errors.New("user ID and exact username do not match")
		}
		result := tx.Model(&user).Updates(map[string]any{"password": passwordHash, "auth_version": gorm.Expr("auth_version + 1")})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		return tx.Create(&model.OperationLog{UserID: &userID, Action: "operator_password_reset", Status: "success", Detail: "origin=local-maintenance; all user sessions revoked"}).Error
	})
}
