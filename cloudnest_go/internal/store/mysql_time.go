package store

import (
	"errors"
	"fmt"
	"strings"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
)

var ErrMySQLTimeZone = errors.New("MYSQL_DSN timestamp location and session time_zone must agree")

// mysqlTimeDSN leaves the driver's interpretation of historical cells intact.
// The session default is installed by the driver on every physical connection,
// so DB-generated DATETIME fields use the same wall-clock convention as loc.
func mysqlTimeDSN(raw string, now time.Time) (string, error) {
	cfg, err := mysqldriver.ParseDSN(raw)
	if err != nil {
		return "", errors.New("invalid MYSQL_DSN")
	}
	location := cfg.Loc
	if location == nil {
		location = time.UTC
	}
	local := now.In(location)
	_, end := local.ZoneBounds()
	if !end.IsZero() {
		// A naive DATETIME cannot distinguish the repeated hour at a DST fold.
		// Do not choose an offset or reinterpret an existing deployment silently.
		return "", fmt.Errorf("%w: use UTC or a location with no future offset transitions; legacy cells require an explicit migration plan", ErrMySQLTimeZone)
	}
	_, offset := local.Zone()
	if offset%60 != 0 || offset < -(13*3600+59*60) || offset > 14*3600 {
		return "", fmt.Errorf("%w: unsupported fixed offset", ErrMySQLTimeZone)
	}
	sign := "+"
	if offset < 0 {
		sign = "-"
		offset = -offset
	}
	value := fmt.Sprintf("'%s%02d:%02d'", sign, offset/3600, offset/60%60)
	if cfg.Params == nil {
		cfg.Params = make(map[string]string)
	}
	for key, configured := range cfg.Params {
		// MySQL system variable names are case insensitive. Reject aliases so
		// duplicate SET assignments cannot undo the contract in map order.
		if strings.EqualFold(key, "time_zone") && (key != "time_zone" || strings.TrimSpace(configured) != value) {
			return "", fmt.Errorf("%w: time_zone must be the quoted fixed offset for loc", ErrMySQLTimeZone)
		}
	}
	cfg.Params["time_zone"] = value
	cfg.ParseTime = true
	return cfg.FormatDSN(), nil
}
