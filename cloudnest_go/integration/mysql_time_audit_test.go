//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"cloudenest_go/internal/store"
)

func TestMySQLTimeAuditCLIReportsRawMetadataWithoutChangingRows(t *testing.T) {
	f := newFixture(t)
	if err := f.db.Exec(`CREATE TABLE timestamp_audit_probe (
		id BIGINT PRIMARY KEY,
		business_time DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
		instant_time TIMESTAMP(6) NULL DEFAULT NULL)`).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.db.Exec("DROP TABLE timestamp_audit_probe") })
	const original = "2020-02-03 04:05:06.123456"
	if err := f.db.Exec("INSERT INTO timestamp_audit_probe(id,business_time,instant_time) VALUES (1,?,NULL)", original).Error; err != nil {
		t.Fatal(err)
	}
	var schemaBefore int64
	if err := f.db.Raw("SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE()").Scan(&schemaBefore).Error; err != nil {
		t.Fatal(err)
	}
	var logsBefore int64
	if err := f.db.Table("operation_log").Count(&logsBefore).Error; err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "cloudnest")
	if output, err := exec.Command("go", "build", "-o", binary, "../cmd/server").CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v %s", err, output)
	}
	command := exec.Command(binary, "audit-mysql-time")
	command.Dir = t.TempDir()
	// The audit neither starts HTTP/workers nor needs Redis, JWT, DATA_DIR,
	// the application schema checks or a project .env file.
	command.Env = []string{"MYSQL_DSN=" + f.cfg.MySQLDSN}
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("audit CLI failed: %v %s", err, stderr.String())
	}
	var report store.MySQLTimeAudit
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Version != 1 || report.ServerUUID == "" || report.Database != "cloudnest_test" || report.SessionZone != "+00:00" || report.OffsetSeconds != 0 || report.SessionNow != report.UTCNow || report.GeneratedAt.IsZero() || report.Truncated || report.ProvenanceNote == "" {
		t.Fatalf("missing identity, time contract or provenance boundary: %#v", report)
	}
	var serverUUID, globalZone, systemZone string
	if err := f.db.Raw("SELECT @@server_uuid,@@global.time_zone,@@system_time_zone").Row().Scan(&serverUUID, &globalZone, &systemZone); err != nil {
		t.Fatal(err)
	}
	if report.ServerUUID != serverUUID || report.GlobalZone != globalZone || report.SystemZone != systemZone {
		t.Fatal("server settings were lost or changed")
	}
	seen := map[string]store.MySQLTimeColumn{}
	last := ""
	for _, column := range report.Columns {
		key := column.Table + "." + column.Column
		if key <= last {
			t.Fatal("unstable or duplicated inventory order")
		}
		last = key
		if column.Table == "timestamp_audit_probe" {
			seen[column.Column] = column
		}
	}
	datetime, timestamp := seen["business_time"], seen["instant_time"]
	if datetime.DataType != "datetime" || datetime.Precision == nil || *datetime.Precision != 6 || datetime.Nullable != "NO" || datetime.Default == nil || !strings.Contains(strings.ToLower(*datetime.Default), "current_timestamp") || !strings.Contains(strings.ToLower(datetime.Extra), "on update") || timestamp.DataType != "timestamp" || timestamp.Default != nil || timestamp.Nullable != "YES" {
		t.Fatalf("temporal column definitions were conflated: %#v", seen)
	}
	var raw string
	var nullInstant bool
	if err := f.db.Raw("SELECT CAST(business_time AS CHAR),instant_time IS NULL FROM timestamp_audit_probe WHERE id=1").Row().Scan(&raw, &nullInstant); err != nil || raw != original || !nullInstant {
		t.Fatal("audit rewrote row values", err)
	}
	var schemaAfter, logsAfter int64
	if err := f.db.Raw("SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE()").Scan(&schemaAfter).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Table("operation_log").Count(&logsAfter).Error; err != nil {
		t.Fatal(err)
	}
	if schemaBefore != schemaAfter || logsBefore != logsAfter || strings.Contains(stdout.String(), original) || strings.Contains(stdout.String(), "cloudnest-disposable-test") {
		t.Fatal("audit changed schema/logs, inspected row values or disclosed credentials")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.AuditMySQLTime(cancelled, f.db); err == nil {
		t.Fatal("cancelled inventory appeared successful")
	}
}

func TestMySQLTimeAuditBoundsLargeVisibleSchemas(t *testing.T) {
	f := newFixture(t)
	// Two empty tables keep each InnoDB row well below its physical limit.
	for n := range 2 {
		table := fmt.Sprintf("timestamp_audit_wide_%d", n)
		var columns []string
		for i := range 510 {
			columns = append(columns, fmt.Sprintf("`t%03d` DATETIME NULL", i))
		}
		if err := f.db.Exec("CREATE TABLE `" + table + "` (" + strings.Join(columns, ",") + ") ENGINE=InnoDB").Error; err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { f.db.Exec("DROP TABLE `" + table + "`") })
	}
	report, err := store.AuditMySQLTime(context.Background(), f.db)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Truncated || len(report.Columns) != report.ColumnLimit || report.ColumnLimit != 1000 {
		t.Fatal("large inventory was unbounded or reported as complete")
	}
	binary := filepath.Join(t.TempDir(), "cloudnest")
	if output, err := exec.Command("go", "build", "-o", binary, "../cmd/server").CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v %s", err, output)
	}
	command := exec.Command(binary, "audit-mysql-time")
	command.Dir = t.TempDir()
	command.Env = []string{"MYSQL_DSN=" + f.cfg.MySQLDSN}
	output, err := command.Output()
	if exit, ok := err.(*exec.ExitError); !ok || !strings.Contains(string(exit.Stderr), store.ErrMySQLTimeAuditTruncated.Error()) {
		t.Fatal("truncated CLI inventory did not return its explicit failure")
	}
	var partial store.MySQLTimeAudit
	if err := json.Unmarshal(output, &partial); err != nil || !partial.Truncated || len(partial.Columns) != partial.ColumnLimit {
		t.Fatal("truncated CLI lost the bounded partial report", err)
	}
}
