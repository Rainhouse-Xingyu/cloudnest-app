package mysql

import (
	"errors"
	"testing"

	"cloudenest_go/internal/service/contract"
	mysqldriver "github.com/go-sql-driver/mysql"
)

func TestNameConstraintErrorClassification(t *testing.T) {
	for _, message := range []string{"Duplicate entry '-1-a' for key 'file.uq_file_active_name_v1'", "Duplicate entry '-1-a' for key 'uq_file_active_name_v1'"} {
		if !errors.Is(normalizeFileNameConstraintError(&mysqldriver.MySQLError{Number: 1062, Message: message}), contract.ErrConflict) {
			t.Fatal("known unique index did not map to conflict")
		}
	}
	for _, original := range []error{
		&mysqldriver.MySQLError{Number: 1062, Message: "Duplicate entry 'uq_file_active_name_v1' for key 'some_other_index'"},
		&mysqldriver.MySQLError{Number: 1062, Message: "Duplicate entry 'a' for key 'file.uq_file_active_name_v1_other'"},
		&mysqldriver.MySQLError{Number: 1213, Message: "uq_file_active_name_v1"},
		errors.New("database connection failed"),
	} {
		if got := normalizeFileNameConstraintError(original); got != original {
			t.Fatal("unrelated database failure was reclassified")
		}
	}
}
