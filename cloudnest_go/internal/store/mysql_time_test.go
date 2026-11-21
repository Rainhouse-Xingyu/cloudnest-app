package store

import (
	"errors"
	"strings"
	"testing"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
)

func TestMySQLTimestampContractPreservesLocationAndConnectionOptions(t *testing.T) {
	for _, tc := range []struct{ location, want string }{
		{"UTC", "'+00:00'"}, {"Etc/GMT-8", "'+08:00'"}, {"America/Phoenix", "'-07:00'"}, {"Asia/Shanghai", "'+08:00'"},
	} {
		cfg := mysqldriver.NewConfig()
		var err error
		cfg.Loc, err = time.LoadLocation(tc.location)
		if err != nil {
			t.Fatal(err)
		}
		cfg.User = "fixture-user"
		cfg.Passwd = "fixture-password"
		cfg.Net = "tcp"
		cfg.Addr = "127.0.0.1:3306"
		cfg.DBName = "test"
		cfg.Params = map[string]string{"charset": "utf8mb4"}
		cfg.ReadTimeout = 7 * time.Second
		cfg.RejectReadOnly = true
		normalized, err := mysqlTimeDSN(cfg.FormatDSN(), time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC))
		if err != nil {
			t.Fatal(err)
		}
		got, err := mysqldriver.ParseDSN(normalized)
		if err != nil {
			t.Fatal(err)
		}
		if got.Loc.String() != cfg.Loc.String() || got.Params["time_zone"] != tc.want || !strings.Contains(normalized, "charset=utf8mb4") || !got.ParseTime || got.User != cfg.User || got.Passwd != cfg.Passwd || got.ReadTimeout != cfg.ReadTimeout || !got.RejectReadOnly {
			t.Fatalf("connection options or timestamp profile changed for %s", tc.location)
		}
		// An already-normalized DSN remains stable, including its explicit
		// matching session variable; repeated opens do not alter the profile.
		if again, err := mysqlTimeDSN(normalized, time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)); err != nil || again != normalized {
			t.Fatal("timestamp profile normalization is not idempotent", err)
		}
	}
}

func TestMySQLTimestampContractRejectsConflictsAndDSTWithoutLeakingDSN(t *testing.T) {
	for _, raw := range []string{
		"user:fixture-secret@tcp(127.0.0.1:3306)/db?loc=UTC&time_zone=%27SYSTEM%27",
		"user:fixture-secret@tcp(127.0.0.1:3306)/db?loc=UTC&time_zone=%27%2B08:00%27",
		"user:fixture-secret@tcp(127.0.0.1:3306)/db?loc=UTC&TIME_ZONE=%27SYSTEM%27",
		"user:fixture-secret@tcp(127.0.0.1:3306)/db?loc=UTC&time_zone=%27%2B00:00%27&Time_Zone=%27%2B08:00%27",
		"user:fixture-secret@tcp(127.0.0.1:3306)/db?loc=America%2FNew_York",
		"user:fixture-secret@tcp(127.0.0.1:3306)/db?loc=Europe%2FBerlin",
	} {
		_, err := mysqlTimeDSN(raw, time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC))
		if !errors.Is(err, ErrMySQLTimeZone) || strings.Contains(err.Error(), "fixture-secret") {
			t.Fatal("unsafe profile accepted or secret exposed")
		}
	}
	_, err := mysqlTimeDSN("invalid-fixture-secret", time.Now())
	if err == nil || strings.Contains(err.Error(), "fixture-secret") {
		t.Fatal("invalid DSN leaked its input")
	}
}
