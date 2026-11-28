package handler

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"testing"

	"cloudenest_go/internal/service/contract"
)

func TestOperationLogCSVCellsAreTextAndPreserveQuotedData(t *testing.T) {
	inputs := []string{"=1+2", " +SUM(1,2)", "\t@formula", "\r-1", "\ufeff=1", "\x00\t=1", "normal,comma", "normal\nnewline", "a\"quote", "success", "2026-11-25 00:00:00"}
	var out bytes.Buffer
	writer := csv.NewWriter(&out)
	values := make([]string, len(inputs))
	for i, value := range inputs {
		values[i] = operationLogCSVCell(value)
	}
	if err := writer.Write(values); err != nil {
		t.Fatal(err)
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		t.Fatal(err)
	}
	read, err := csv.NewReader(&out).Read()
	if err != nil {
		t.Fatal(err)
	}
	for i, value := range read {
		want := inputs[i]
		if i < 6 {
			want = "'" + want
		}
		if value != want {
			t.Fatalf("CSV text boundary %d: got %q want %q", i, value, want)
		}
	}
}

func TestOperationLogCSVBuildIsBoundedAndCancellationWritesNothing(t *testing.T) {
	var out bytes.Buffer
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	writer := &boundedAuditWriter{out: &out, ctx: cancelled}
	if n, err := writer.Write([]byte("private audit bytes")); n != 0 || !errors.Is(err, context.Canceled) || out.Len() != 0 {
		t.Fatal("cancelled audit export wrote", n, err)
	}
	writer = &boundedAuditWriter{out: &out, ctx: context.Background(), written: operationLogExportBytes - 1}
	if n, err := writer.Write([]byte("xx")); n != 0 || !errors.Is(err, contract.ErrTransferSpace) || out.Len() != 0 {
		t.Fatal("audit export exceeded bound", n, err)
	}
}
