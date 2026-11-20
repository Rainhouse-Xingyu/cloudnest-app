package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"gorm.io/gorm"
)

const mysqlTimeAuditColumnLimit = 1000

var ErrMySQLTimeAuditTruncated = errors.New("timestamp inventory exceeds the report limit; no data was changed")

type MySQLTimeColumn struct {
	Table      string  `json:"table" gorm:"column:table_name"`
	Column     string  `json:"column" gorm:"column:column_name"`
	DataType   string  `json:"data_type"`
	ColumnType string  `json:"column_type"`
	Precision  *int64  `json:"precision" gorm:"column:datetime_precision"`
	Nullable   string  `json:"nullable" gorm:"column:is_nullable"`
	Default    *string `json:"default" gorm:"column:column_default"`
	Extra      string  `json:"extra"`
}

type MySQLTimeAudit struct {
	Version        int               `json:"version"`
	GeneratedAt    time.Time         `json:"generated_at"`
	ServerUUID     string            `json:"server_uuid"`
	Database       string            `json:"database"`
	SessionZone    string            `json:"session_zone"`
	GlobalZone     string            `json:"global_zone"`
	SystemZone     string            `json:"system_zone"`
	SessionNow     string            `json:"session_now"`
	UTCNow         string            `json:"utc_now"`
	OffsetSeconds  int64             `json:"offset_seconds"`
	Scope          string            `json:"scope"`
	ColumnLimit    int               `json:"column_limit"`
	Truncated      bool              `json:"truncated"`
	Columns        []MySQLTimeColumn `json:"columns" gorm:"-"`
	ProvenanceNote string            `json:"provenance_note"`
}

// AuditMySQLTime reads visible metadata only. In particular it never infers a
// historical source zone from the current server zone or reads through loc's
// interpretation of existing cells. DDL must be quiesced for a stable inventory;
// information_schema metadata is not a historical consistent snapshot.
func AuditMySQLTime(ctx context.Context, db *gorm.DB) (MySQLTimeAudit, error) {
	report := MySQLTimeAudit{
		Version: 1, GeneratedAt: time.Now().UTC(), ColumnLimit: mysqlTimeAuditColumnLimit,
		Scope:          "visible_datetime_and_timestamp_columns_in_current_database",
		ProvenanceNote: "Current zones and defaults do not establish historical provenance. No row values were inspected or changed. Column visibility depends on database privileges.",
		Columns:        []MySQLTimeColumn{},
	}
	err := db.WithContext(ctx).Session(&gorm.Session{NewDB: true}).Transaction(func(tx *gorm.DB) error {
		if err := tx.Raw(`SELECT @@server_uuid AS server_uuid,DATABASE() AS `+"`database`"+`,
			@@session.time_zone AS session_zone,@@global.time_zone AS global_zone,
			@@system_time_zone AS system_zone,
			DATE_FORMAT(NOW(6),'%Y-%m-%d %H:%i:%s.%f') AS session_now,
			DATE_FORMAT(UTC_TIMESTAMP(6),'%Y-%m-%d %H:%i:%s.%f') AS utc_now,
			TIMESTAMPDIFF(SECOND,UTC_TIMESTAMP(),NOW()) AS offset_seconds`).Row().Scan(
			&report.ServerUUID, &report.Database, &report.SessionZone, &report.GlobalZone,
			&report.SystemZone, &report.SessionNow, &report.UTCNow, &report.OffsetSeconds); err != nil {
			return err
		}
		if report.ServerUUID == "" || report.Database == "" {
			return errors.New("timestamp inventory requires a selected database and server identity")
		}
		return tx.Raw(`SELECT table_name,column_name,data_type,column_type,
			datetime_precision,is_nullable,column_default,extra
			FROM information_schema.columns
			WHERE table_schema=DATABASE() AND data_type IN ('datetime','timestamp')
			ORDER BY table_name,column_name LIMIT ?`, mysqlTimeAuditColumnLimit+1).Scan(&report.Columns).Error
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return MySQLTimeAudit{}, err
	}
	if len(report.Columns) > mysqlTimeAuditColumnLimit {
		report.Truncated = true
		report.Columns = report.Columns[:mysqlTimeAuditColumnLimit]
	}
	return report, nil
}
