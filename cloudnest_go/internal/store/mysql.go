package store

import (
	"errors"
	"fmt"

	"cloudenest_go/internal/config"
	"cloudenest_go/model"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// NewMySQL函数用于创建一个新的MySQL数据库连接，接受配置对象作为参数，返回数据库连接对象和错误信息。函数首先使用gorm.Open函数连接MySQL数据库，如果连接失败则返回错误信息。然后调用AutoMigrate方法自动迁移数据库表结构，包括User、Role、UserRole和UserPathPermission等模型。如果迁移失败则返回错误信息。最后返回成功的数据库连接对象。
func NewMySQL(cfg config.Config) (*gorm.DB, error) {
	db, err := gorm.Open(mysql.Open(cfg.MySQLDSN), &gorm.Config{})
	if err != nil {
		return nil, fmt.Errorf("connect mysql failed: %w", err)
	}

	if err := db.AutoMigrate(
		&model.User{},
		&model.Role{},
		&model.UserRole{},
		&model.UserPathPermission{},
		&model.Storage{},
		&model.Workspace{},
		&model.WorkspaceUser{},
		&model.File{},
		&model.FileVersion{},
		&model.FilePermission{},
		&model.UploadTask{},
		&model.UploadChunk{},
		&model.Share{},
		&model.OperationLog{},
		&model.LivePhoto{},
		&model.Device{},
		&model.FrpMapping{},
		&model.FrpConfig{},
		&model.AiMetadata{},
		&model.AiTask{},
	); err != nil {
		return nil, fmt.Errorf("auto migrate auth tables failed: %w", err)
	}

	return db, nil
}

// SeedAdminUser函数用于在数据库中创建一个默认的管理员用户，如果该用户不存在的话。函数首先查询数据库中是否存在用户名为"admin"的用户，如果查询失败且不是记录未找到错误，则返回错误信息。如果用户不存在，则使用bcrypt生成密码哈希，并创建一个新的用户记录，用户名为"admin"，密码为哈希值，状态为1（启用）。然后查询数据库中是否存在名为"admin"的角色，如果查询失败且不是记录未找到错误，则返回错误信息。如果角色不存在，则创建一个新的角色记录，角色名为"admin"，描述为"system administrator"。最后查询数据库中是否存在该用户和角色的关联记录，如果查询失败且不是记录未找到错误，则返回错误信息。如果关联记录不存在，则创建一个新的用户角色关联记录，将管理员用户和管理员角色关联起来。函数最后返回nil表示成功。
func SeedAdminUser(db *gorm.DB) error {
	var adminRole model.Role
	err := db.Where("role_name = ?", "admin").First(&adminRole).Error
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		adminRole = model.Role{RoleName: "admin", Description: "system administrator"}
		if err := db.Create(&adminRole).Error; err != nil {
			return err
		}
	}

	var admin model.User
	err = db.Where("username = ?", "admin").First(&admin).Error
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		hash, hashErr := bcrypt.GenerateFromPassword([]byte("admin"), bcrypt.DefaultCost)
		if hashErr != nil {
			return hashErr
		}
		admin = model.User{
			Username: "admin",
			Password: string(hash),
			Status:   1,
		}
		if err := db.Create(&admin).Error; err != nil {
			return err
		}
	}

	var userRole model.UserRole
	err = db.Where("user_id = ? AND role_id = ?", admin.ID, adminRole.ID).First(&userRole).Error
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err := db.Create(&model.UserRole{UserID: admin.ID, RoleID: adminRole.ID}).Error; err != nil {
			return err
		}
	}

	return nil
}
