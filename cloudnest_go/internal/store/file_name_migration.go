package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const FileNameMigrationVersion = "20261022_01_file_active_names"
const fileNameScopeColumn = "cn_active_scope_v1"
const fileNameValueColumn = "cn_active_name_v1"
const fileNameUniqueIndex = "uq_file_active_name_v1"
const fileNameScalarCheck = "cn_file_scalar_v1"
const fileNameScopeExpression = "if(is_deleted=0,if(parent_id=0,-user_id,parent_id),null)"
const fileNameValueExpression = "if(is_deleted=0,filename,null)"
const fileNameScalarExpression = "user_id is not null and user_id>0 and parent_id is not null and parent_id>=0 and filename is not null and char_length(filename)>0 and is_folder is not null and is_folder in (0,1) and is_deleted is not null and is_deleted in (0,1)"

var safeSQLIdentifier = regexp.MustCompile(`^[a-zA-Z0-9_]{1,64}$`)

type fileNameColumn struct {
	Name       string `gorm:"column:column_name"`
	DataType   string
	ColumnType string
	IsNullable string
	MaxLength  sql.NullInt64 `gorm:"column:character_maximum_length"`
	Charset    string        `gorm:"column:character_set_name"`
	Collation  string        `gorm:"column:collation_name"`
	Expression string        `gorm:"column:generation_expression"`
	Extra      string
}

type fileNameIndexPart struct {
	ColumnName string
	NonUnique  int
	SeqInIndex int
	SubPart    sql.NullInt64
	IsVisible  string
}

type fileNameCheck struct {
	Clause   string
	Enforced string
}
type fileNameLedger struct{ Checksum string }

type fileNameDefinition struct {
	base    fileNameColumn
	columns []fileNameColumn
	index   []fileNameIndexPart
	checks  []fileNameCheck
	ledger  []fileNameLedger
}

func readFileNameDefinition(db *gorm.DB) (fileNameDefinition, error) {
	db = db.Session(&gorm.Session{NewDB: true})
	var d fileNameDefinition
	var base []fileNameColumn
	const columns = "column_name,data_type,column_type,is_nullable,character_maximum_length,character_set_name,collation_name,generation_expression,extra"
	if err := db.Raw("SELECT " + columns + " FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='file' AND column_name='filename'").Scan(&base).Error; err != nil {
		return d, err
	}
	if len(base) != 1 || base[0].DataType != "varchar" || base[0].MaxLength.Int64 != 255 || !safeSQLIdentifier.MatchString(base[0].Charset) || !safeSQLIdentifier.MatchString(base[0].Collation) {
		return d, errors.New("file.filename must be VARCHAR(255) with a supported explicit character set and collation")
	}
	d.base = base[0]
	if err := db.Raw("SELECT "+columns+" FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='file' AND column_name IN (?,?) ORDER BY column_name", fileNameScopeColumn, fileNameValueColumn).Scan(&d.columns).Error; err != nil {
		return d, err
	}
	if err := db.Raw("SELECT column_name,non_unique,seq_in_index,sub_part,is_visible FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='file' AND index_name=? ORDER BY seq_in_index", fileNameUniqueIndex).Scan(&d.index).Error; err != nil {
		return d, err
	}
	if err := db.Raw("SELECT cc.check_clause AS clause,tc.enforced FROM information_schema.table_constraints tc JOIN information_schema.check_constraints cc ON cc.constraint_schema=tc.constraint_schema AND cc.constraint_name=tc.constraint_name WHERE tc.table_schema=DATABASE() AND tc.table_name='file' AND tc.constraint_name=? AND tc.constraint_type='CHECK'", fileNameScalarCheck).Scan(&d.checks).Error; err != nil {
		return d, err
	}
	var tables int64
	if err := db.Raw("SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='cloudnest_schema_migration'").Scan(&tables).Error; err != nil {
		return d, err
	}
	if tables > 0 {
		if err := db.Raw("SELECT checksum FROM cloudnest_schema_migration WHERE version=? LIMIT 2", FileNameMigrationVersion).Scan(&d.ledger).Error; err != nil {
			return d, err
		}
	}
	return d, nil
}

func (d fileNameDefinition) ddl() string {
	return fmt.Sprintf("ALTER TABLE `file` ADD COLUMN `%s` BIGINT GENERATED ALWAYS AS (%s) VIRTUAL, ADD COLUMN `%s` VARCHAR(255) CHARACTER SET %s COLLATE %s GENERATED ALWAYS AS (%s) VIRTUAL, ADD CONSTRAINT `%s` CHECK (%s) ENFORCED, ADD UNIQUE INDEX `%s` (`%s`,`%s`)", fileNameScopeColumn, fileNameScopeExpression, fileNameValueColumn, d.base.Charset, d.base.Collation, fileNameValueExpression, fileNameScalarCheck, fileNameScalarExpression, fileNameUniqueIndex, fileNameScopeColumn, fileNameValueColumn)
}

func (d fileNameDefinition) checksum() string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(d.ddl())))
}
func (d fileNameDefinition) hasObjects() bool { return len(d.columns)+len(d.index)+len(d.checks) > 0 }
func normalizedSQLExpression(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || r == '`' || r == '(' || r == ')' {
			return -1
		}
		return unicode.ToLower(r)
	}, s)
}

func (d fileNameDefinition) validateObjects() error {
	if len(d.columns) != 2 || len(d.index) != 2 || len(d.checks) != 1 {
		return errors.New("file-name constraint objects are missing or partial")
	}
	for _, c := range d.columns {
		if c.IsNullable != "YES" || !strings.Contains(strings.ToUpper(c.Extra), "VIRTUAL GENERATED") {
			return errors.New("file-name columns are not virtual generated columns")
		}
		switch c.Name {
		case fileNameScopeColumn:
			if c.DataType != "bigint" || strings.Contains(strings.ToLower(c.ColumnType), "unsigned") || normalizedSQLExpression(c.Expression) != normalizedSQLExpression(fileNameScopeExpression) {
				return errors.New("file-name namespace expression differs from the migration")
			}
		case fileNameValueColumn:
			if c.DataType != "varchar" || c.MaxLength.Int64 != 255 || c.Charset != d.base.Charset || c.Collation != d.base.Collation || normalizedSQLExpression(c.Expression) != normalizedSQLExpression(fileNameValueExpression) {
				return errors.New("file-name value expression or collation differs from the migration")
			}
		default:
			return errors.New("unexpected file-name generated column")
		}
	}
	for i, part := range d.index {
		expected := fileNameScopeColumn
		if i == 1 {
			expected = fileNameValueColumn
		}
		if part.ColumnName != expected || part.SeqInIndex != i+1 || part.NonUnique != 0 || part.SubPart.Valid || part.IsVisible != "YES" {
			return errors.New("file-name index is not the expected complete visible unique index")
		}
	}
	if d.checks[0].Enforced != "YES" || normalizedSQLExpression(d.checks[0].Clause) != normalizedSQLExpression(fileNameScalarExpression) {
		return errors.New("file-name scalar check is changed or not enforced")
	}
	return nil
}

// CheckFileNameSchema is read-only and validates definitions, not just names or
// a version stamp. A deleted/altered constraint cannot masquerade as an upgrade.
func CheckFileNameSchema(db *gorm.DB) error {
	d, err := readFileNameDefinition(db)
	if err != nil {
		return fmt.Errorf("file-name schema check failed: %w", err)
	}
	if err := d.validateObjects(); err != nil {
		return fmt.Errorf("%w; back up, audit and run explicit migrate-file-names before starting", err)
	}
	if len(d.ledger) != 1 || d.ledger[0].Checksum != d.checksum() {
		return errors.New("file-name migration ledger missing or checksum mismatch; inspect schema before starting")
	}
	return nil
}

// MigrateFileNames adds only this versioned constraint. It never repairs, renames
// or deletes historical file rows. One InnoDB ALTER installs all four objects;
// the subsequent ledger write is separately recoverable because DDL auto-commits.
func MigrateFileNames(ctx context.Context, db *gorm.DB) error {
	return db.WithContext(ctx).Connection(func(conn *gorm.DB) (result error) {
		// Keep Raw/Scan statements independent on this fixed connection. Reusing
		// an initialized GORM statement can contaminate the audit subquery.
		conn = conn.Session(&gorm.Session{NewDB: true})
		var database string
		if err := conn.Raw("SELECT DATABASE()").Scan(&database).Error; err != nil {
			return err
		}
		digest := sha256.Sum256([]byte(database))
		lockName := fmt.Sprintf("cn:file-names:%x", digest[:16])
		var acquired sql.NullInt64
		if err := conn.Raw("SELECT GET_LOCK(?,10)", lockName).Scan(&acquired).Error; err != nil {
			return err
		}
		if !acquired.Valid || acquired.Int64 != 1 {
			return errors.New("another file-name migration holds the database maintenance lock")
		}
		defer func() {
			releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var released sql.NullInt64
			err := conn.WithContext(releaseCtx).Raw("SELECT RELEASE_LOCK(?)", lockName).Scan(&released).Error
			if err != nil || !released.Valid || released.Int64 != 1 {
				// Never return a session with a possibly retained advisory lock to the pool.
				if sqlConn, ok := conn.Statement.ConnPool.(*sql.Conn); ok {
					_ = sqlConn.Raw(func(any) error { return driver.ErrBadConn })
				}
				result = errors.Join(result, errors.New("file-name maintenance lock release failed"))
			}
		}()
		var version, engine string
		if err := conn.Raw("SELECT VERSION()").Scan(&version).Error; err != nil {
			return err
		}
		var major, minor, patch int
		if n, _ := fmt.Sscanf(version, "%d.%d.%d", &major, &minor, &patch); n != 3 || major != 8 || (minor == 0 && patch < 16) || strings.Contains(strings.ToLower(version), "mariadb") {
			return errors.New("file-name migration requires MySQL 8.0.16 or newer in the MySQL 8 family")
		}
		if err := conn.Raw("SELECT engine FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='file'").Scan(&engine).Error; err != nil {
			return err
		}
		if engine != "InnoDB" {
			return errors.New("file-name migration requires an existing InnoDB file table")
		}
		d, err := readFileNameDefinition(conn)
		if err != nil {
			return err
		}
		if len(d.ledger) > 1 || (len(d.ledger) == 1 && d.ledger[0].Checksum != d.checksum()) {
			return errors.New("file-name migration checksum mismatch; refusing to overwrite migration history")
		}
		if d.hasObjects() {
			if err := d.validateObjects(); err != nil {
				return err
			}
			if len(d.ledger) == 1 {
				return nil
			}
			// Crash after the atomic ALTER but before the ledger INSERT: adopt only an
			// exact definition match; never silently complete a partial or altered DDL.
		} else {
			if len(d.ledger) > 0 {
				return errors.New("file-name migration is recorded but its constraints are missing; refusing automatic repair")
			}
			report, err := AuditFileNames(ctx, conn)
			if err != nil {
				return err
			}
			if report.HasFindings() {
				return ErrFileNameAuditFindings
			}
			// Keep driver errors out of logs: duplicate-key text can contain filenames.
			quiet := conn.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)})
			if err := quiet.Exec(d.ddl()).Error; err != nil {
				return errors.New("file-name ALTER failed; original file rows were not repaired; inspect schema and rerun the read-only audit")
			}
			current, err := readFileNameDefinition(conn)
			if err != nil {
				return err
			}
			if err := current.validateObjects(); err != nil {
				return err
			}
		}
		if err := conn.Exec("CREATE TABLE IF NOT EXISTS cloudnest_schema_migration (version VARCHAR(96) NOT NULL PRIMARY KEY, checksum CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL, applied_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)) ENGINE=InnoDB").Error; err != nil {
			return err
		}
		if err := conn.Exec("INSERT INTO cloudnest_schema_migration (version,checksum) VALUES (?,?)", FileNameMigrationVersion, d.checksum()).Error; err != nil {
			return err
		}
		return CheckFileNameSchema(conn)
	})
}
