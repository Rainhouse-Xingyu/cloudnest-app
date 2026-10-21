package store

import (
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"cloudenest_go/internal/config"
	"cloudenest_go/model"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// NewMySQL opens a connection only. Schema changes require the explicit migrate command.
func NewMySQL(cfg config.Config) (*gorm.DB, error) {
	db, err := gorm.Open(mysql.Open(cfg.MySQLDSN), &gorm.Config{
		Logger: logger.New(log.New(os.Stderr, "", log.LstdFlags), logger.Config{
			SlowThreshold: time.Second, LogLevel: logger.Warn,
			IgnoreRecordNotFoundError: true, ParameterizedQueries: true,
		}),
	})
	if err != nil {
		return nil, fmt.Errorf("connect mysql failed: %w", err)
	}
	return db, nil
}

// Migrate applies the legacy model schema only when explicitly requested.
// Versioned migrations and upgrade/rollback rehearsal remain a separate milestone.
func Migrate(db *gorm.DB) error {
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
		&model.SystemSetting{},
		&model.LivePhoto{},
		&model.Device{},
		&model.FrpMapping{},
		&model.FrpConfig{},
		&model.AiMetadata{},
		&model.AiTask{},
	); err != nil {
		return fmt.Errorf("migrate schema failed: %w", err)
	}

	return nil
}

// BootstrapAdminUser only initializes an empty user table. It never upgrades an
// existing account or resets a password on restart. The unique username also
// makes concurrent bootstrap attempts fail atomically.
func BootstrapAdminUser(db *gorm.DB, password string) error {
	if err := ValidateBootstrapPassword(password); err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash bootstrap password failed")
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&model.User{}).Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return errors.New("bootstrap requires an empty user table; existing accounts are never modified")
		}
		admin := model.User{Username: "admin", Password: string(hash), Status: 1}
		if err := tx.Create(&admin).Error; err != nil {
			return err
		}
		var role model.Role
		if err := tx.Where("role_name = ?", "admin").First(&role).Error; err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			role = model.Role{RoleName: "admin", Description: "system administrator"}
			if err := tx.Create(&role).Error; err != nil {
				return err
			}
		}
		return tx.Create(&model.UserRole{UserID: admin.ID, RoleID: role.ID}).Error
	})
}

func ValidateBootstrapPassword(password string) error {
	if len(password) < 14 || len(password) > 72 || strings.TrimSpace(password) != password || strings.Contains(strings.ToLower(password), "change_me") {
		return errors.New("CLOUDNEST_BOOTSTRAP_PASSWORD must contain 14 to 72 bytes without surrounding whitespace; use a unique random password")
	}
	return nil
}
