package store

import (
	"errors"
	"testing"
	"time"
)

func offsetPointer(value string) *string { return &value }

func TestTimestampPlanResolvesActualInstantsAcrossZoneBoundaries(t *testing.T) {
	for _, tc := range []struct {
		wall, source string
		offset       *string
		target       string
		want         string
	}{
		{"2026-12-31 23:59:59.123456", "America/Phoenix", nil, "UTC", "2027-01-01 06:59:59.123456"},
		{"2027-01-01 00:10:00.000001", "Asia/Kathmandu", nil, "UTC", "2026-12-31 18:25:00.000001"},
		{"2026-11-01 01:30:00.000000", "America/New_York", offsetPointer("-04:00"), "UTC", "2026-11-01 05:30:00.000000"},
		{"2026-11-01 01:30:00.000000", "America/New_York", offsetPointer("-05:00"), "UTC", "2026-11-01 06:30:00.000000"},
		{"2026-11-20 00:00:00.654321", "+08:00", nil, "America/Phoenix", "2026-11-19 09:00:00.654321"},
		{"1800-01-01 00:00:00.000000", "+00:00:52", nil, "UTC", "1799-12-31 23:59:08.000000"},
	} {
		t.Run(tc.source+tc.wall+tc.want, func(t *testing.T) {
			target, err := time.LoadLocation(tc.target)
			if err != nil {
				t.Fatal(err)
			}
			after, instant, offset, err := convertMySQLWallTime(tc.wall, MySQLTimeSelection{SourceZone: tc.source, SourceOffset: tc.offset}, target)
			if err != nil || after != tc.want || offset == "" {
				t.Fatalf("conversion: %s %v", after, err)
			}
			parsed, err := time.Parse(time.RFC3339Nano, instant)
			if err != nil || parsed.In(target).Format(mysqlWallTime) != after {
				t.Fatal("instant and target fields disagree", err)
			}
		})
	}
}

func TestTimestampPlanRejectsGuessesGapsFoldsAndOverflow(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		wall, source string
		offset       *string
		target       *time.Location
	}{
		{"2026-03-08 02:30:00.000000", "America/New_York", nil, time.UTC},
		{"2026-11-01 01:30:00.000000", "America/New_York", nil, time.UTC},
		{"2026-11-01 01:30:00.000000", "America/New_York", offsetPointer("-07:00"), time.UTC},
		{"2026-11-01 05:30:00.000000", "UTC", nil, ny},
		{"0000-00-00 00:00:00.000000", "UTC", nil, time.UTC},
		{"2026-02-30 12:00:00.000000", "UTC", nil, time.UTC},
		{"2026-11-20 12:00:00.000000", "Local", nil, time.UTC},
		{"2026-11-20 12:00:00.000000", "SYSTEM", nil, time.UTC},
		{"2026-11-20 12:00:00.000000", "", nil, time.UTC},
		{"1000-01-01 00:00:00.000000", "+08:00", nil, time.UTC},
		{"9999-12-31 23:59:59.999999", "-07:00", nil, time.UTC},
	} {
		if _, _, _, err := convertMySQLWallTime(tc.wall, MySQLTimeSelection{SourceZone: tc.source, SourceOffset: tc.offset}, tc.target); !errors.Is(err, ErrMySQLTimePlan) {
			t.Fatal("unsafe timestamp conversion accepted", tc.source, tc.wall)
		}
	}
	for _, value := range []string{"", "08:00", "+8:00", "+24:00", "+01:60", "+01:01:60", "+01:01.00", "+0x:00", "-00:00:0x", "+01:00 ", " UTC "} {
		if _, err := parseTimeOffset(value); !errors.Is(err, ErrMySQLTimePlan) {
			t.Fatal("invalid offset accepted")
		}
	}
}
