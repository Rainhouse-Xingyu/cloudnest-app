package store

import (
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"cloudenest_go/internal/auth"
	"cloudenest_go/internal/config"
	"cloudenest_go/model"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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
// File-name and storage-reference constraints use separate explicit versioned migrations. Other
// model migrations and real-data upgrade/rollback rehearsal remain later work.
func Migrate(db *gorm.DB) error {
	if err := db.AutoMigrate(
		&model.User{},
		&model.Role{},
		&model.UserRole{},
		&model.UserPathPermission{},
		&model.Storage{},
		&model.StorageReadLease{},
		&model.StorageGarbage{},
		&model.Workspace{},
		&model.WorkspaceUser{},
		&model.File{},
		&model.FileNamespace{},
		&model.FileVersion{},
		&model.FilePermission{},
		&model.UploadTask{},
		&model.TransferVolume{},
		&model.TransferArtifact{},
		&model.StorageQuotaPolicy{},
		&model.UserStorageLedger{},
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
		&model.AIIndexJob{},
	); err != nil {
		return fmt.Errorf("migrate schema failed: %w", err)
	}

	return db.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.StorageQuotaPolicy{ID: 1}).Error
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
	return auth.ValidateNewPassword(password)
}

// CheckAuthSchema is read-only. Older schemas must be explicitly migrated before
// serving requests; never issue sessions with a silently missing version column.
func CheckAuthSchema(db *gorm.DB) error {
	var count int64
	if err := db.Raw("SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'user' AND column_name = 'auth_version'").Scan(&count).Error; err != nil {
		return err
	}
	if count != 1 {
		return errors.New("authentication schema missing auth_version; back up and run the explicit migrate command before starting")
	}
	return nil
}

func CheckFileSchema(db *gorm.DB) error {
	var count int64
	if err := db.Raw("SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'file_namespace' AND column_name = 'namespace_key'").Scan(&count).Error; err != nil {
		return err
	}
	if count != 1 {
		return errors.New("file namespace schema missing; back up and run the explicit migrate command before starting")
	}
	if err := CheckFileNameSchema(db); err != nil {
		return err
	}
	if err := db.Raw("SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='file_version' AND column_name IN ('file_type','source_verified')").Scan(&count).Error; err != nil {
		return err
	}
	if count != 2 {
		return errors.New("version metadata schema missing file_type/source_verified; back up and run explicit migrate before starting")
	}
	if err := CheckStorageReferenceSchema(db); err != nil {
		return err
	}
	if err := CheckStorageLeaseSchema(db); err != nil {
		return err
	}
	if err := CheckStorageGCSchema(db); err != nil {
		return err
	}
	if err := CheckQuotaSchema(db); err != nil {
		return err
	}
	if err := CheckUploadResultSchema(db); err != nil {
		return err
	}
	if err := CheckUploadChunkSchema(db); err != nil {
		return err
	}
	if err := CheckTransferSchema(db); err != nil {
		return err
	}
	return CheckAIIndexSchema(db)
}
