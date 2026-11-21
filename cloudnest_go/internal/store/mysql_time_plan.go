package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	mysqldriver "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

const MySQLTimePlanCellLimit = 200

const mysqlWallTime = "2006-01-02 15:04:05.000000"

var ErrMySQLTimePlan = errors.New("timestamp plan rejected; no data was changed")

// These legacy metadata columns use the business DATETIME convention. Explicit
// UTC leases, AI capture wall times, expiry fields and ledger clocks are excluded.
var mysqlTimePlanColumns = map[string][]string{
	"user": {"create_time", "update_time"}, "storage": {"create_time"},
	"workspace": {"create_time"}, "file": {"create_time", "update_time"},
	"upload_chunk": {"create_time"}, "upload_task": {"create_time"},
	"user_path_permission": {"create_time"}, "file_version": {"create_time"},
	"live_photo": {"create_time"}, "device": {"create_time"},
	"frp_mapping": {"create_time"}, "frp_config": {"create_time"},
	"ai_metadata": {"create_time"}, "ai_task": {"create_time"},
	"share": {"create_time"}, "operation_log": {"create_time"},
}

type MySQLTimeSelection struct {
	Table        string  `json:"table"`
	Column       string  `json:"column"`
	RowID        int64   `json:"row_id"`
	SourceZone   string  `json:"source_zone"`
	SourceOffset *string `json:"source_offset,omitempty"`
	Evidence     string  `json:"evidence"`
}

type MySQLTimePlanRequest struct {
	Version int                  `json:"version"`
	Cells   []MySQLTimeSelection `json:"cells"`
}

type MySQLTimePlannedCell struct {
	MySQLTimeSelection
	Definition           MySQLTimeColumn `json:"definition"`
	Before               *string         `json:"before"`
	After                *string         `json:"after"`
	Instant              *string         `json:"instant"`
	ResolvedSourceOffset *string         `json:"resolved_source_offset"`
}

type MySQLTimePlan struct {
	Version     int                    `json:"version"`
	GeneratedAt time.Time              `json:"generated_at"`
	ServerUUID  string                 `json:"server_uuid"`
	Database    string                 `json:"database"`
	TargetZone  string                 `json:"target_zone"`
	SessionZone string                 `json:"session_zone"`
	Cells       []MySQLTimePlannedCell `json:"cells"`
	SHA256      string                 `json:"sha256"`
}

func sourceTimeLocation(name string) (*time.Location, error) {
	if name == "" || name == "Local" || len(name) > 128 {
		return nil, fmt.Errorf("%w: explicit source zone required", ErrMySQLTimePlan)
	}
	if name[0] == '+' || name[0] == '-' {
		offset, err := parseTimeOffset(name)
		if err != nil {
			return nil, err
		}
		return time.FixedZone(name, offset), nil
	}
	location, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("%w: unknown source zone", ErrMySQLTimePlan)
	}
	return location, nil
}

func parseTimeOffset(value string) (int, error) {
	invalid := fmt.Errorf("%w: offset must be signed HH:MM or HH:MM:SS", ErrMySQLTimePlan)
	if (len(value) != 6 && len(value) != 9) || (value[0] != '+' && value[0] != '-') || value[3] != ':' || (len(value) == 9 && value[6] != ':') {
		return 0, invalid
	}
	for i, r := range value {
		if i != 0 && i != 3 && i != 6 && (r < '0' || r > '9') {
			return 0, invalid
		}
	}
	hour, _ := strconv.Atoi(value[1:3])
	minute, _ := strconv.Atoi(value[4:6])
	second := 0
	if len(value) == 9 {
		second, _ = strconv.Atoi(value[7:9])
	}
	if hour > 23 || minute > 59 || second > 59 {
		return 0, invalid
	}
	offset := hour*3600 + minute*60 + second
	if value[0] == '-' {
		offset = -offset
	}
	return offset, nil
}

func formatTimeOffset(offset int) string {
	sign := "+"
	if offset < 0 {
		sign, offset = "-", -offset
	}
	result := fmt.Sprintf("%s%02d:%02d", sign, offset/3600, offset/60%60)
	if offset%60 != 0 {
		result += fmt.Sprintf(":%02d", offset%60)
	}
	return result
}

// Enumerate actual zone intervals around the wall fields, rather than relying
// on time.Date's unspecified choice during a fold or normalizing a missing hour.
func wallTimeCandidates(wall time.Time, location *time.Location) []time.Time {
	start, end := wall.Add(-48*time.Hour), wall.Add(48*time.Hour)
	offsets := map[int]bool{}
	for cursor := start; !cursor.After(end); {
		local := cursor.In(location)
		_, offset := local.Zone()
		offsets[offset] = true
		_, next := local.ZoneBounds()
		if next.IsZero() || next.After(end) || !next.After(cursor) {
			break
		}
		cursor = next
	}
	var candidates []time.Time
	for offset := range offsets {
		candidate := wall.Add(-time.Duration(offset) * time.Second)
		if candidate.In(location).Format(mysqlWallTime) == wall.Format(mysqlWallTime) {
			candidates = append(candidates, candidate)
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Before(candidates[j]) })
	return candidates
}

func convertMySQLWallTime(raw string, selection MySQLTimeSelection, target *time.Location) (string, string, string, error) {
	wall, err := time.Parse(mysqlWallTime, raw)
	if err != nil || len(raw) != 26 || wall.Format(mysqlWallTime) != raw || wall.Year() < 1000 || wall.Year() > 9999 {
		return "", "", "", fmt.Errorf("%w: invalid or zero DATETIME", ErrMySQLTimePlan)
	}
	source, err := sourceTimeLocation(selection.SourceZone)
	if err != nil {
		return "", "", "", err
	}
	candidates := wallTimeCandidates(wall, source)
	if selection.SourceOffset != nil {
		wanted, err := parseTimeOffset(*selection.SourceOffset)
		if err != nil {
			return "", "", "", err
		}
		filtered := candidates[:0]
		for _, candidate := range candidates {
			_, offset := candidate.In(source).Zone()
			if offset == wanted {
				filtered = append(filtered, candidate)
			}
		}
		candidates = filtered
	}
	if len(candidates) != 1 {
		return "", "", "", fmt.Errorf("%w: source time is missing, ambiguous or disagrees with its explicit offset", ErrMySQLTimePlan)
	}
	instant := candidates[0]
	local := instant.In(target)
	if local.Year() < 1000 || local.Year() > 9999 || instant.Year() < 1 || instant.Year() > 9999 {
		return "", "", "", fmt.Errorf("%w: converted timestamp is outside supported ranges", ErrMySQLTimePlan)
	}
	after := local.Format(mysqlWallTime)
	targetWall, _ := time.Parse(mysqlWallTime, after)
	// The target DATETIME must survive a later read without needing a separate
	// offset column. In particular never create another ambiguous wall value.
	reconstructed := wallTimeCandidates(targetWall, target)
	if len(reconstructed) != 1 || !reconstructed[0].Equal(instant) {
		return "", "", "", fmt.Errorf("%w: target wall time cannot represent the instant uniquely", ErrMySQLTimePlan)
	}
	_, offset := instant.In(source).Zone()
	return after, instant.UTC().Format(time.RFC3339Nano), formatTimeOffset(offset), nil
}

// PlanMySQLTime converts only explicitly selected, provenance-backed legacy
// metadata cells. It never changes rows/schema, expands a range, or guesses a
// source zone from the current MySQL server. The final DSN must be the one used
// to open db; its profile is checked against this transaction's session zone.
func PlanMySQLTime(ctx context.Context, db *gorm.DB, dsn string, request MySQLTimePlanRequest) (MySQLTimePlan, error) {
	if request.Version != 1 || len(request.Cells) == 0 || len(request.Cells) > MySQLTimePlanCellLimit {
		return MySQLTimePlan{}, ErrMySQLTimePlan
	}
	normalized, err := mysqlTimeDSN(dsn, time.Now())
	if err != nil {
		return MySQLTimePlan{}, err
	}
	profile, err := mysqldriver.ParseDSN(normalized)
	if err != nil || profile.Loc.String() == "Local" {
		return MySQLTimePlan{}, fmt.Errorf("%w: target loc must name an explicit reproducible location", ErrMySQLTimePlan)
	}
	selections := append([]MySQLTimeSelection(nil), request.Cells...)
	sort.Slice(selections, func(i, j int) bool {
		a, b := selections[i], selections[j]
		if a.Table != b.Table {
			return a.Table < b.Table
		}
		if a.RowID != b.RowID {
			return a.RowID < b.RowID
		}
		return a.Column < b.Column
	})
	for i, selection := range selections {
		allowed := false
		for _, column := range mysqlTimePlanColumns[selection.Table] {
			allowed = allowed || selection.Column == column
		}
		if !allowed || selection.RowID <= 0 || !utf8.ValidString(selection.Evidence) || len(selection.Evidence) == 0 || len(selection.Evidence) > 200 || strings.TrimSpace(selection.Evidence) != selection.Evidence || strings.ContainsFunc(selection.Evidence, unicode.IsControl) {
			return MySQLTimePlan{}, fmt.Errorf("%w: select a supported metadata cell and provide an evidence reference", ErrMySQLTimePlan)
		}
		if i > 0 && selections[i-1].Table == selection.Table && selections[i-1].RowID == selection.RowID && selections[i-1].Column == selection.Column {
			return MySQLTimePlan{}, fmt.Errorf("%w: duplicate cell", ErrMySQLTimePlan)
		}
		if _, err := sourceTimeLocation(selection.SourceZone); err != nil {
			return MySQLTimePlan{}, err
		}
		if selection.SourceOffset != nil {
			if _, err := parseTimeOffset(*selection.SourceOffset); err != nil {
				return MySQLTimePlan{}, err
			}
		}
	}
	plan := MySQLTimePlan{Version: 1, GeneratedAt: time.Now().UTC(), TargetZone: profile.Loc.String(), Cells: []MySQLTimePlannedCell{}}
	err = db.WithContext(ctx).Session(&gorm.Session{NewDB: true}).Transaction(func(tx *gorm.DB) error {
		if err := tx.Raw("SELECT @@server_uuid,DATABASE(),@@session.time_zone").Row().Scan(&plan.ServerUUID, &plan.Database, &plan.SessionZone); err != nil {
			return err
		}
		if plan.ServerUUID == "" || plan.Database != profile.DBName || "'"+plan.SessionZone+"'" != profile.Params["time_zone"] {
			return fmt.Errorf("%w: database/session differs from target profile", ErrMySQLTimePlan)
		}
		for _, selection := range selections {
			var definitions []MySQLTimeColumn
			if err := tx.Raw(`SELECT table_name,column_name,data_type,column_type,datetime_precision,is_nullable,column_default,extra
				FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name=? AND column_name=?`, selection.Table, selection.Column).Scan(&definitions).Error; err != nil {
				return err
			}
			if len(definitions) != 1 || definitions[0].DataType != "datetime" || definitions[0].Precision == nil || *definitions[0].Precision < 0 || *definitions[0].Precision > 6 || strings.Contains(strings.ToUpper(definitions[0].Extra), "VIRTUAL") || strings.Contains(strings.ToUpper(definitions[0].Extra), "STORED GENERATED") {
				return fmt.Errorf("%w: selected column is not a supported stored DATETIME", ErrMySQLTimePlan)
			}
			var engine string
			if err := tx.Raw("SELECT engine FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name=? AND table_type='BASE TABLE'", selection.Table).Scan(&engine).Error; err != nil {
				return err
			}
			var primary []string
			if err := tx.Raw("SELECT column_name FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name=? AND index_name='PRIMARY' ORDER BY seq_in_index", selection.Table).Scan(&primary).Error; err != nil {
				return err
			}
			if !strings.EqualFold(engine, "InnoDB") || len(primary) != 1 || primary[0] != "id" {
				return fmt.Errorf("%w: table requires an InnoDB id primary key", ErrMySQLTimePlan)
			}
			var idType string
			if err := tx.Raw("SELECT data_type FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name=? AND column_name='id'", selection.Table).Scan(&idType).Error; err != nil {
				return err
			}
			switch idType {
			case "tinyint", "smallint", "mediumint", "int", "bigint":
			default:
				// Comparing a numeric parameter to a VARCHAR primary key can match
				// several different strings through MySQL's numeric coercion.
				return fmt.Errorf("%w: id must be an integer primary key", ErrMySQLTimePlan)
			}
			// Identifiers above come only from the code-owned allowlist. DATE_FORMAT
			// returns original wall fields, bypassing the driver's loc conversion.
			var raw sql.NullString
			var isNull bool
			query := "SELECT `" + selection.Column + "` IS NULL,DATE_FORMAT(`" + selection.Column + "`,'%Y-%m-%d %H:%i:%s.%f') FROM `" + selection.Table + "` WHERE id=?"
			if err := tx.Raw(query, selection.RowID).Row().Scan(&isNull, &raw); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return fmt.Errorf("%w: selected row unavailable", ErrMySQLTimePlan)
				}
				return err
			}
			if isNull == raw.Valid {
				return fmt.Errorf("%w: invalid DATETIME cannot be treated as NULL", ErrMySQLTimePlan)
			}
			cell := MySQLTimePlannedCell{MySQLTimeSelection: selection, Definition: definitions[0]}
			if raw.Valid {
				after, instant, offset, err := convertMySQLWallTime(raw.String, selection, profile.Loc)
				if err != nil {
					return fmt.Errorf("%w (%s.%s id=%d)", err, selection.Table, selection.Column, selection.RowID)
				}
				before := raw.String
				cell.Before, cell.After, cell.Instant, cell.ResolvedSourceOffset = &before, &after, &instant, &offset
			}
			plan.Cells = append(plan.Cells, cell)
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return MySQLTimePlan{}, err
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		return MySQLTimePlan{}, err
	}
	plan.SHA256 = fmt.Sprintf("%x", sha256.Sum256(encoded))
	return plan, nil
}
