package mysql

import (
	"context"

	"cloudenest_go/model"

	"gorm.io/gorm"
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

func (r *AuthRepo) UpdateUserPassword(ctx context.Context, userID int64, password string) error {
	return r.db.WithContext(ctx).Model(&model.User{}).Where("id = ?", userID).Update("password", password).Error
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
		Select("id", "username", "email", "status", "create_time", "update_time").
		First(&u, userID).Error
	if err != nil {
		return model.User{}, err
	}
	return u, nil
}
