package model

import (
	"database/sql/driver"
	"errors"
	"time"
)

// CommandTime stores UTC wall-clock DATETIME values independently of the DSN's
// loc option. Existing business timestamps retain their historical convention.
type CommandTime struct{ time.Time }

func (t CommandTime) Value() (driver.Value, error) {
	if t.IsZero() {
		return nil, nil
	}
	return t.UTC().Format("2006-01-02 15:04:05.999999"), nil
}
func (t *CommandTime) Scan(value any) error {
	var parsed time.Time
	switch v := value.(type) {
	case nil:
		t.Time = time.Time{}
		return nil
	case time.Time:
		// The driver may have attached a local offset to the stored UTC fields.
		parsed = time.Date(v.Year(), v.Month(), v.Day(), v.Hour(), v.Minute(), v.Second(), v.Nanosecond(), time.UTC)
	case []byte:
		return t.Scan(string(v))
	case string:
		var err error
		parsed, err = time.Parse("2006-01-02 15:04:05.999999", v)
		if err != nil {
			return errors.New("invalid command timestamp")
		}
	default:
		return errors.New("invalid command timestamp")
	}
	t.Time = parsed
	return nil
}
