package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const FileStorageMigrationVersion = "20261022_02_file_storage_refs"
const VersionStorageMigrationVersion = "20261022_03_version_storage_refs"

type storageReferenceStep struct {
	version, table, constraint, index string
}

var storageReferenceSteps = []storageReferenceStep{
	{FileStorageMigrationVersion, "file", "cn_file_storage_v1", "idx_cn_file_storage_v1"},
	{VersionStorageMigrationVersion, "file_version", "cn_version_storage_v1", "idx_cn_version_storage_v1"},
}

func (s storageReferenceStep) ddl() string {
	// Every identifier is an internal constant, never operator input.
	return fmt.Sprintf("ALTER TABLE `%s` ADD INDEX `%s` (`storage_id`), ADD CONSTRAINT `%s` FOREIGN KEY (`storage_id`) REFERENCES `storage` (`id`) ON DELETE RESTRICT ON UPDATE RESTRICT", s.table, s.index, s.constraint)
}

func (s storageReferenceStep) checksum() string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(s.ddl())))
}

type storageForeignKey struct {
	ConstraintName                                                               string
	ColumnName, ReferencedTableSchema, ReferencedTableName, ReferencedColumnName string
	OrdinalPosition                                                              int
	DeleteRule, UpdateRule                                                       string
}

type storageReferenceDefinition struct {
	keys   []storageForeignKey
	index  []fileNameIndexPart
	ledger []fileNameLedger
}

func readStorageReferenceDefinition(db *gorm.DB, step storageReferenceStep) (storageReferenceDefinition, error) {
	db = db.Session(&gorm.Session{NewDB: true})
	var d storageReferenceDefinition
	if err := db.Raw(`SELECT k.constraint_name,k.column_name,k.referenced_table_schema,k.referenced_table_name,k.referenced_column_name,k.ordinal_position,r.delete_rule,r.update_rule
		FROM information_schema.key_column_usage k JOIN information_schema.referential_constraints r
		ON r.constraint_schema=k.constraint_schema AND r.constraint_name=k.constraint_name AND r.table_name=k.table_name
		WHERE k.table_schema=DATABASE() AND k.table_name=? AND (k.constraint_name=? OR k.constraint_name IN (
			SELECT constraint_name FROM information_schema.key_column_usage WHERE table_schema=DATABASE() AND table_name=? AND column_name='storage_id'
		)) ORDER BY k.constraint_name,k.ordinal_position`, step.table, step.constraint, step.table).Scan(&d.keys).Error; err != nil {
		return d, err
	}
	if err := db.Raw("SELECT column_name,non_unique,seq_in_index,sub_part,is_visible FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name=? AND index_name=? ORDER BY seq_in_index", step.table, step.index).Scan(&d.index).Error; err != nil {
		return d, err
	}
	var tables int64
	if err := db.Raw("SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='cloudnest_schema_migration'").Scan(&tables).Error; err != nil {
		return d, err
	}
	if tables != 0 {
		if err := db.Raw("SELECT checksum FROM cloudnest_schema_migration WHERE version=? LIMIT 2", step.version).Scan(&d.ledger).Error; err != nil {
			return d, err
		}
	}
	return d, nil
}

func (d storageReferenceDefinition) hasObjects() bool { return len(d.keys)+len(d.index) > 0 }

func (d storageReferenceDefinition) validateObjects(database, expectedConstraint string) error {
	if len(d.keys) != 1 || len(d.index) != 1 {
		return errors.New("storage reference constraint or index is missing or partial")
	}
	k, i := d.keys[0], d.index[0]
	if k.ConstraintName != expectedConstraint || k.ColumnName != "storage_id" || k.OrdinalPosition != 1 || k.ReferencedTableSchema != database || k.ReferencedTableName != "storage" || k.ReferencedColumnName != "id" || k.DeleteRule != "RESTRICT" || k.UpdateRule != "RESTRICT" {
		return errors.New("storage reference must target this database's storage.id with DELETE/UPDATE RESTRICT")
	}
	if i.ColumnName != "storage_id" || i.NonUnique != 1 || i.SeqInIndex != 1 || i.SubPart.Valid || i.IsVisible != "YES" {
		return errors.New("storage reference index is not the expected complete visible nonunique index")
	}
	return nil
}

func checkStorageReferenceBase(db *gorm.DB) (string, error) {
	db = db.Session(&gorm.Session{NewDB: true})
	var database, version string
	var sessionChecks, globalChecks int
	if err := db.Raw("SELECT DATABASE()").Scan(&database).Error; err != nil {
		return "", err
	}
	if err := db.Raw("SELECT @@SESSION.foreign_key_checks").Scan(&sessionChecks).Error; err != nil {
		return "", err
	}
	if err := db.Raw("SELECT @@GLOBAL.foreign_key_checks").Scan(&globalChecks).Error; err != nil {
		return "", err
	}
	if sessionChecks != 1 || globalChecks != 1 {
		return "", errors.New("storage references require session and global foreign_key_checks=1; no setting was changed")
	}
	if err := db.Raw("SELECT VERSION()").Scan(&version).Error; err != nil {
		return "", err
	}
	var major, minor, patch int
	if n, _ := fmt.Sscanf(version, "%d.%d.%d", &major, &minor, &patch); n != 3 || major != 8 || (minor == 0 && patch < 16) || strings.Contains(strings.ToLower(version), "mariadb") {
		return "", errors.New("storage references require MySQL 8.0.16 or newer in the MySQL 8 family")
	}
	for _, table := range []string{"storage", "file", "file_version"} {
		var engine string
		if err := db.Raw("SELECT engine FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name=?", table).Scan(&engine).Error; err != nil {
			return "", err
		}
		if engine != "InnoDB" {
			return "", errors.New("storage references require existing InnoDB storage, file and file_version tables")
		}
		column, nullable := "storage_id", "YES"
		if table == "storage" {
			column, nullable = "id", "NO"
		} else if table == "file_version" {
			nullable = "NO"
		}
		var columns []fileNameColumn
		if err := db.Raw("SELECT column_name,data_type,column_type,is_nullable,extra FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name=? AND column_name=?", table, column).Scan(&columns).Error; err != nil {
			return "", err
		}
		if len(columns) != 1 || columns[0].DataType != "bigint" || strings.Contains(strings.ToLower(columns[0].ColumnType), "unsigned") || columns[0].IsNullable != nullable || strings.Contains(strings.ToUpper(columns[0].Extra), "GENERATED") {
			return "", errors.New("storage reference columns differ from the expected signed BIGINT/nullability schema; run and review explicit model migration")
		}
	}
	var primary []fileNameIndexPart
	if err := db.Raw("SELECT column_name,non_unique,seq_in_index,sub_part,is_visible FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='storage' AND index_name='PRIMARY' ORDER BY seq_in_index").Scan(&primary).Error; err != nil {
		return "", err
	}
	if len(primary) != 1 || primary[0].ColumnName != "id" || primary[0].NonUnique != 0 || primary[0].SubPart.Valid || primary[0].IsVisible != "YES" {
		return "", errors.New("storage.id must be the complete visible primary key")
	}
	return database, nil
}

// CheckStorageReferenceSchema is read-only. It verifies actual definitions and
// per-step checksums. A migration marker alone never grants permission to run.
func CheckStorageReferenceSchema(db *gorm.DB) error {
	database, err := checkStorageReferenceBase(db)
	if err != nil {
		return err
	}
	for _, step := range storageReferenceSteps {
		d, err := readStorageReferenceDefinition(db, step)
		if err != nil {
			return err
		}
		if err := d.validateObjects(database, step.constraint); err != nil {
			return fmt.Errorf("%s: %w; audit and run explicit migrate-storage-references before starting", step.version, err)
		}
		if len(d.ledger) != 1 || d.ledger[0].Checksum != step.checksum() {
			return errors.New("storage reference migration ledger missing or checksum mismatch; inspect schema before starting")
		}
	}
	return nil
}

// MigrateStorageReferences never repairs historical references. Each child table
// is a separate atomic ALTER and ledger entry; cross-table DDL is not atomic.
// A retry resumes only exact completed steps, including a DDL/ledger crash gap.
func MigrateStorageReferences(ctx context.Context, db *gorm.DB) error {
	return db.WithContext(ctx).Connection(func(conn *gorm.DB) (result error) {
		conn = conn.Session(&gorm.Session{NewDB: true})
		var database string
		if err := conn.Raw("SELECT DATABASE()").Scan(&database).Error; err != nil {
			return err
		}
		digest := sha256.Sum256([]byte(database))
		lockName := fmt.Sprintf("cn:storage-refs:%x", digest[:16])
		var acquired sql.NullInt64
		if err := conn.Raw("SELECT GET_LOCK(?,10)", lockName).Scan(&acquired).Error; err != nil {
			// The server may have acquired the lock even if its response was lost.
			// Never put an uncertain session back into the connection pool.
			if sqlConn, ok := conn.Statement.ConnPool.(*sql.Conn); ok {
				_ = sqlConn.Raw(func(any) error { return driver.ErrBadConn })
			}
			return err
		}
		if !acquired.Valid || acquired.Int64 != 1 {
			return errors.New("another storage-reference migration holds the database maintenance lock")
		}
		defer func() {
			releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var released sql.NullInt64
			err := conn.WithContext(releaseCtx).Raw("SELECT RELEASE_LOCK(?)", lockName).Scan(&released).Error
			if err != nil || !released.Valid || released.Int64 != 1 {
				if sqlConn, ok := conn.Statement.ConnPool.(*sql.Conn); ok {
					_ = sqlConn.Raw(func(any) error { return driver.ErrBadConn })
				}
				result = errors.Join(result, errors.New("storage-reference maintenance lock release failed"))
			}
		}()
		database, err := checkStorageReferenceBase(conn)
		if err != nil {
			return err
		}
		// Validate every step before writing anything. Never auto-repair drift in
		// the second table after having silently changed the first one.
		definitions := make([]storageReferenceDefinition, len(storageReferenceSteps))
		for n, step := range storageReferenceSteps {
			d, err := readStorageReferenceDefinition(conn, step)
			if err != nil {
				return err
			}
			if len(d.ledger) > 1 || (len(d.ledger) == 1 && d.ledger[0].Checksum != step.checksum()) {
				return errors.New("storage-reference migration checksum mismatch; refusing to overwrite history")
			}
			if d.hasObjects() {
				if err := d.validateObjects(database, step.constraint); err != nil {
					return err
				}
			} else if len(d.ledger) > 0 {
				return errors.New("recorded storage-reference constraints are missing; refusing automatic repair")
			}
			definitions[n] = d
		}
		report, err := AuditStorageReferences(ctx, conn)
		if err != nil {
			return err
		}
		if report.HasFindings() {
			return ErrStorageReferenceFindings
		}
		if err := conn.Exec("CREATE TABLE IF NOT EXISTS cloudnest_schema_migration (version VARCHAR(96) NOT NULL PRIMARY KEY, checksum CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL, applied_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)) ENGINE=InnoDB").Error; err != nil {
			return err
		}
		for n, step := range storageReferenceSteps {
			d := definitions[n]
			if !d.hasObjects() {
				quiet := conn.Session(&gorm.Session{NewDB: true, Logger: logger.Default.LogMode(logger.Silent)})
				if err := quiet.Exec(step.ddl()).Error; err != nil {
					return fmt.Errorf("%s ALTER failed; inspect schema and rerun the read-only storage-reference audit; earlier completed steps remain installed", step.version)
				}
				current, err := readStorageReferenceDefinition(conn, step)
				if err != nil {
					return err
				}
				if err := current.validateObjects(database, step.constraint); err != nil {
					return err
				}
			}
			if len(d.ledger) == 0 {
				if err := conn.Exec("INSERT INTO cloudnest_schema_migration (version,checksum) VALUES (?,?)", step.version, step.checksum()).Error; err != nil {
					return err
				}
			}
		}
		return CheckStorageReferenceSchema(conn)
	})
}
