//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"cloudenest_go/internal/store"
	mysqldriver "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

func TestMySQLTimePlanCLIConvertsExplicitMixedSourcesWithoutWriting(t *testing.T) {
	f := newFixture(t)
	// Exercise microseconds in an explicitly upgraded disposable legacy field.
	if err := f.db.Exec("ALTER TABLE file MODIFY create_time DATETIME(6) NULL DEFAULT CURRENT_TIMESTAMP(6)").Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.db.Exec("ALTER TABLE file MODIFY create_time DATETIME NULL DEFAULT CURRENT_TIMESTAMP").Error; err != nil {
			t.Error(err)
		}
	})
	a := fixtureFolder(t, f, f.alice, "time-plan-a", 0)
	b := fixtureFolder(t, f, f.bob, "time-plan-b", 0)
	untouched := fixtureFolder(t, f, f.alice, "time-plan-untouched", 0)
	if err := f.db.Exec("UPDATE file SET create_time=?,update_time=? WHERE id=?", "2026-11-19 23:39:31.123456", "2026-11-20 00:30:00", a.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Exec("UPDATE file SET create_time=NULL,update_time=update_time WHERE id=?", b.ID).Error; err != nil {
		t.Fatal(err)
	}
	request := store.MySQLTimePlanRequest{Version: 1, Cells: []store.MySQLTimeSelection{
		{Table: "file", Column: "create_time", RowID: b.ID, SourceZone: "America/New_York", Evidence: "CHG-test/null-import"},
		{Table: "file", Column: "update_time", RowID: a.ID, SourceZone: "+08:00", Evidence: "CHG-test/explicit-writer"},
		{Table: "file", Column: "create_time", RowID: a.ID, SourceZone: "America/Phoenix", Evidence: "CHG-test/mysql-default"},
	}}
	type rawRow struct {
		ID         int64
		CreateTime *string
		UpdateTime *string
	}
	read := func() []rawRow {
		t.Helper()
		var rows []rawRow
		if err := f.db.Raw("SELECT id,CAST(create_time AS CHAR) AS create_time,CAST(update_time AS CHAR) AS update_time FROM file WHERE id IN ? ORDER BY id", []int64{a.ID, b.ID, untouched.ID}).Scan(&rows).Error; err != nil {
			t.Fatal(err)
		}
		return rows
	}
	before := read()
	var logsBefore int64
	if err := f.db.Table("operation_log").Count(&logsBefore).Error; err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "cloudnest")
	if output, err := exec.Command("go", "build", "-o", binary, "../cmd/server").CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v %s", err, output)
	}
	input, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary, "plan-mysql-time")
	command.Dir = t.TempDir()
	command.Env = []string{"MYSQL_DSN=" + f.cfg.MySQLDSN}
	command.Stdin = bytes.NewReader(input)
	output, err := command.Output()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			t.Fatalf("plan CLI failed: %s", exit.Stderr)
		}
		t.Fatal(err)
	}
	var plan store.MySQLTimePlan
	if err := json.Unmarshal(output, &plan); err != nil {
		t.Fatal(err)
	}
	var serverUUID string
	if err := f.db.Raw("SELECT @@server_uuid").Scan(&serverUUID).Error; err != nil {
		t.Fatal(err)
	}
	if plan.Version != 1 || plan.ServerUUID != serverUUID || plan.Database != "cloudnest_test" || plan.TargetZone != "UTC" || plan.SessionZone != "+00:00" || plan.GeneratedAt.IsZero() || len(plan.Cells) != 3 {
		t.Fatal("plan is missing its concrete database/profile binding")
	}
	first, second, third := plan.Cells[0], plan.Cells[1], plan.Cells[2]
	if first.RowID != a.ID || first.Column != "create_time" || first.Before == nil || *first.Before != "2026-11-19 23:39:31.123456" || first.After == nil || *first.After != "2026-11-20 06:39:31.123456" || first.ResolvedSourceOffset == nil || *first.ResolvedSourceOffset != "-07:00" || first.Evidence != request.Cells[2].Evidence || first.Definition.Precision == nil || *first.Definition.Precision != 6 {
		t.Fatal("raw microsecond fields or provenance changed")
	}
	if second.Column != "update_time" || second.After == nil || *second.After != "2026-11-19 16:30:00.000000" || third.RowID != b.ID || third.Before != nil || third.After != nil || third.Instant != nil || third.ResolvedSourceOffset != nil {
		t.Fatal("mixed writer or NULL semantics were conflated")
	}
	digest := plan.SHA256
	plan.SHA256 = ""
	canonical, err := json.Marshal(plan)
	if err != nil || digest != fmt.Sprintf("%x", sha256.Sum256(canonical)) {
		t.Fatal("plan digest does not bind all reviewed fields", err)
	}
	var logsAfter int64
	if err := f.db.Table("operation_log").Count(&logsAfter).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, read()) || logsBefore != logsAfter || strings.Contains(string(output), "cloudnest-disposable-test") {
		t.Fatal("planning wrote rows/audit or exposed connection credentials")
	}
	for _, request := range []store.MySQLTimePlanRequest{
		{Version: 2, Cells: request.Cells},
		{Version: 1},
		{Version: 1, Cells: []store.MySQLTimeSelection{request.Cells[0], request.Cells[0]}},
		{Version: 1, Cells: make([]store.MySQLTimeSelection, store.MySQLTimePlanCellLimit+1)},
	} {
		if _, err := store.PlanMySQLTime(context.Background(), f.db, f.cfg.MySQLDSN, request); !errors.Is(err, store.ErrMySQLTimePlan) {
			t.Fatal("invalid selection set accepted", err)
		}
	}
	for _, selection := range []store.MySQLTimeSelection{
		{Table: "agent_command", Column: "created_at", RowID: 1, SourceZone: "UTC", Evidence: "CHG-1"},
		{Table: "file` WHERE 1=1 --", Column: "create_time", RowID: a.ID, SourceZone: "UTC", Evidence: "CHG-1"},
		{Table: "file", Column: "create_time", RowID: a.ID, SourceZone: "UTC"},
		{Table: "file", Column: "create_time", RowID: a.ID, SourceZone: "UTC", Evidence: "bad\nreference"},
		{Table: "file", Column: "create_time", RowID: 9223372036854775807, SourceZone: "UTC", Evidence: "CHG-1"},
	} {
		if _, err := store.PlanMySQLTime(context.Background(), f.db, f.cfg.MySQLDSN, store.MySQLTimePlanRequest{Version: 1, Cells: []store.MySQLTimeSelection{selection}}); !errors.Is(err, store.ErrMySQLTimePlan) {
			t.Fatal("unsafe metadata target accepted", err)
		}
	}
	profile, err := mysqldriver.ParseDSN(f.cfg.MySQLDSN)
	if err != nil {
		t.Fatal(err)
	}
	profile.DBName = "wrong_database"
	if _, err := store.PlanMySQLTime(context.Background(), f.db, profile.FormatDSN(), request); !errors.Is(err, store.ErrMySQLTimePlan) {
		t.Fatal("mismatched database profile accepted", err)
	}
	profile.DBName = "cloudnest_test"
	profile.Loc, err = time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PlanMySQLTime(context.Background(), f.db, profile.FormatDSN(), request); !errors.Is(err, store.ErrMySQLTimePlan) {
		t.Fatal("mismatched session profile accepted", err)
	}
	localConfig := f.cfg
	localConfig.MySQLDSN = profile.FormatDSN()
	localDB, err := store.NewMySQL(localConfig)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := localDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	localPlan, err := store.PlanMySQLTime(context.Background(), localDB, localConfig.MySQLDSN, store.MySQLTimePlanRequest{Version: 1, Cells: []store.MySQLTimeSelection{request.Cells[2]}})
	if err != nil || localPlan.TargetZone != "Asia/Shanghai" || localPlan.SessionZone != "+08:00" || len(localPlan.Cells) != 1 || localPlan.Cells[0].After == nil || *localPlan.Cells[0].After != "2026-11-20 14:39:31.123456" {
		t.Fatal("explicit target location was replaced or applied twice", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.PlanMySQLTime(cancelled, f.db, f.cfg.MySQLDSN, request); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled plan appeared complete", err)
	}
	if !reflect.DeepEqual(before, read()) {
		t.Fatal("rejected plan changed rows")
	}
	// Legacy permissive writers can have stored zero dates. They are invalid
	// source values, not SQL NULL, and must never receive a plausible plan.
	t.Cleanup(func() {
		if err := f.db.Exec("UPDATE file SET create_time=?,update_time=update_time WHERE id=?", "2026-11-19 23:39:31.123456", a.ID).Error; err != nil {
			t.Error(err)
		}
	})
	if err := f.db.Connection(func(conn *gorm.DB) (result error) {
		var mode string
		if err := conn.Raw("SELECT @@session.sql_mode").Scan(&mode).Error; err != nil {
			return err
		}
		if err := conn.Exec("SET SESSION sql_mode='' ").Error; err != nil {
			return err
		}
		defer func() { result = errors.Join(result, conn.Exec("SET SESSION sql_mode=?", mode).Error) }()
		return conn.Exec("UPDATE file SET create_time='0000-00-00 00:00:00',update_time=update_time WHERE id=?", a.ID).Error
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PlanMySQLTime(context.Background(), f.db, f.cfg.MySQLDSN, store.MySQLTimePlanRequest{Version: 1, Cells: []store.MySQLTimeSelection{request.Cells[2]}}); !errors.Is(err, store.ErrMySQLTimePlan) {
		t.Fatal("legacy zero date was accepted or treated as NULL", err)
	}
}

func TestMySQLTimePlanRejectsTimestampColumnInsteadOfConvertingItAgain(t *testing.T) {
	f := newFixture(t)
	a := fixtureFolder(t, f, f.alice, "time-plan-timestamp", 0)
	if err := f.db.Exec("ALTER TABLE file MODIFY create_time TIMESTAMP NULL DEFAULT CURRENT_TIMESTAMP").Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.db.Exec("ALTER TABLE file MODIFY create_time DATETIME NULL DEFAULT CURRENT_TIMESTAMP").Error; err != nil {
			t.Error(err)
		}
	})
	request := store.MySQLTimePlanRequest{Version: 1, Cells: []store.MySQLTimeSelection{
		{Table: "file", Column: "create_time", RowID: a.ID, SourceZone: "UTC", Evidence: "CHG-1"},
	}}
	if _, err := store.PlanMySQLTime(context.Background(), f.db, f.cfg.MySQLDSN, request); !errors.Is(err, store.ErrMySQLTimePlan) {
		t.Fatal("TIMESTAMP was treated as a naive DATETIME", err)
	}
	var auditID int64
	if err := f.db.Table("operation_log").Select("MIN(id)").Scan(&auditID).Error; err != nil || auditID <= 0 {
		t.Fatal("missing disposable audit row", err)
	}
	if err := f.db.Exec("ALTER TABLE operation_log MODIFY id VARCHAR(20) NOT NULL").Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.db.Exec("ALTER TABLE operation_log MODIFY id BIGINT NOT NULL AUTO_INCREMENT").Error; err != nil {
			t.Error(err)
		}
	})
	request.Cells[0].Table, request.Cells[0].RowID = "operation_log", auditID
	if _, err := store.PlanMySQLTime(context.Background(), f.db, f.cfg.MySQLDSN, request); !errors.Is(err, store.ErrMySQLTimePlan) {
		t.Fatal("numeric coercion on a non-integer primary key was accepted", err)
	}
}
