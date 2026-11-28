package handler

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"cloudenest_go/internal/middleware"
	"cloudenest_go/internal/service"
	"cloudenest_go/internal/service/contract"
	"github.com/gin-gonic/gin"
)

const operationLogExportRows = 5000
const operationLogExportBytes int64 = 16 << 20

// Protect spreadsheet readers from interpreting attacker-controlled cells as
// formulae. CSV quoting alone does not give text semantics to a cell.
func operationLogCSVCell(value string) string {
	visible := strings.TrimLeftFunc(value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) || r == '\ufeff' })
	if visible != "" && strings.ContainsRune("=+-@", rune(visible[0])) {
		return "'" + value
	}
	if value != "" && (value[0] == '\t' || value[0] == '\r' || value[0] == '\n') {
		return "'" + value
	}
	return value
}

type boundedAuditWriter struct {
	out     io.Writer
	ctx     context.Context
	written int64
}

func (w *boundedAuditWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	if int64(len(p)) > operationLogExportBytes-w.written {
		return 0, contract.ErrTransferSpace
	}
	n, err := w.out.Write(p)
	w.written += int64(n)
	return n, err
}

// ExportOperationLogs builds a complete bounded representation in the shared
// temporary registry before sending it with the normal digest/rate/cancel path.
// No historical audit row is changed or deleted.
func (h *FileHandler) ExportOperationLogs(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	uid := c.GetInt64(middleware.ContextUserIDKey)
	isAdmin, _ := c.Get(middleware.ContextIsAdminKey)
	admin, _ := isAdmin.(bool)
	var q OperationLogQuery
	if err := c.ShouldBindQuery(&q); err != nil || q.SnapshotID < 0 || q.CursorMode < 0 || q.CursorMode > 1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid audit export query"})
		return
	}
	if q.CursorMode == 1 && q.All == 1 && !admin {
		c.JSON(http.StatusForbidden, gin.H{"error": "admin only for all=1"})
		return
	}
	q.Action = strings.TrimSpace(q.Action)
	q.Status = strings.TrimSpace(q.Status)
	q.StartTime = strings.TrimSpace(q.StartTime)
	q.EndTime = strings.TrimSpace(q.EndTime)
	if q.Limit <= 0 || q.Limit > operationLogExportRows {
		q.Limit = operationLogExportRows
	}
	rate, err := h.transferRate(c, "download")
	if err != nil {
		writeFileMutationError(c, err, "download policy unavailable")
		return
	}
	// Admit before loading up to 5001 rows, so rejected concurrent exports do
	// not allocate their result sets outside the shared generation budget.
	artifact, err := h.Transfers.CreateArchive(c.Request.Context(), uid)
	if err != nil {
		writeFileMutationError(c, err, "audit export capacity unavailable")
		return
	}
	defer releaseArchive(artifact)
	build, stop := context.WithTimeout(artifact.Context(), time.Minute)
	defer stop()
	rows, err := h.Svc.ExportOperationLogs(build, uid, admin, service.FileOperationLogQuery{Action: q.Action, Status: q.Status, StartTime: q.StartTime, EndTime: q.EndTime, Limit: q.Limit + 1, All: q.All, SnapshotID: q.SnapshotID})
	if err != nil {
		if strings.Contains(err.Error(), "invalid start_time") || strings.Contains(err.Error(), "invalid end_time") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid audit export dates"})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "audit export query failed"})
		}
		return
	}
	truncated := len(rows) > q.Limit
	if truncated {
		rows = rows[:q.Limit]
	}
	writer := csv.NewWriter(&boundedAuditWriter{out: artifact, ctx: build})
	if err := writer.Write([]string{"id", "user_id", "file_id", "action", "status", "detail", "client_ip", "create_time"}); err != nil {
		writeFileMutationError(c, err, "audit export failed")
		return
	}
	for _, row := range rows {
		userID, fileID := "", ""
		if row.UserID != nil {
			userID = strconv.FormatInt(*row.UserID, 10)
		}
		if row.FileID != nil {
			fileID = strconv.FormatInt(*row.FileID, 10)
		}
		values := []string{strconv.FormatInt(row.ID, 10), userID, fileID, row.Action, row.Status, row.Detail, row.ClientIP, row.CreateTime.UTC().Format("2006-01-02 15:04:05")}
		for i := range values {
			values[i] = operationLogCSVCell(values[i])
		}
		if err := writer.Write(values); err != nil {
			writeFileMutationError(c, err, "audit export failed")
			return
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		writeFileMutationError(c, err, "audit export failed")
		return
	}
	if err := artifact.SealArchive(); err != nil {
		writeFileMutationError(c, err, "audit export failed")
		return
	}
	snapshot := q.SnapshotID
	if snapshot == 0 && len(rows) > 0 {
		snapshot = rows[0].ID
	}
	scope := "self"
	if admin && q.All == 1 {
		scope = "all"
	}
	c.Header("X-CloudNest-Audit-Scope", scope)
	c.Header("X-CloudNest-Audit-User", strconv.FormatInt(uid, 10))
	c.Header("X-CloudNest-Audit-Snapshot", strconv.FormatInt(snapshot, 10))
	c.Header("X-CloudNest-Audit-Rows", strconv.Itoa(len(rows)))
	c.Header("X-CloudNest-Audit-Truncated", strconv.FormatBool(truncated))
	c.Header("X-CloudNest-Audit-Timezone", "UTC")
	c.Header("Content-Type", "text/csv; charset=utf-8")
	name := fmt.Sprintf("operation_logs_%s.csv", time.Now().UTC().Format("20060102T150405Z"))
	if err := h.sendArchiveWithRateLimit(c, artifact, name, rate, nil); err != nil {
		writeDownloadError(c, err)
	}
}
