package handler

import (
	"cloudenest_go/internal/middleware"
	"cloudenest_go/internal/service/contract"
	"github.com/gin-gonic/gin"
	"net/http"
	"strconv"
	"strings"
)

func (h *FileHandler) PauseUpload(c *gin.Context)  { h.controlUpload(c, true) }
func (h *FileHandler) ResumeUpload(c *gin.Context) { h.controlUpload(c, false) }
func (h *FileHandler) controlUpload(c *gin.Context, paused bool) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16<<10)
	var req struct {
		UploadID string `json:"upload_id" binding:"required"`
		Expected *int64 `json:"expected_epoch" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Expected == nil || *req.Expected < 0 {
		c.JSON(400, gin.H{"error": "upload_id and nonnegative expected_epoch required"})
		return
	}
	uid := c.GetInt64(middleware.ContextUserIDKey)
	task, err := h.Svc.ControlUpload(c.Request.Context(), uid, req.UploadID, *req.Expected, paused, c.ClientIP())
	if err != nil {
		writeFileMutationError(c, err, "upload control failed")
		return
	}
	h.Transfers.CancelBefore(uid, task.ID, task.TransferEpoch)
	c.JSON(200, gin.H{"upload_id": task.UploadID, "status": task.Status, "transfer_epoch": task.TransferEpoch, "expires_at": task.ExpiresAt})
}

// Once a task has been controlled, old clients/queued requests must not acquire
// a new generation implicitly just because metadata arrived after resume.
func verifyUploadEpoch(c *gin.Context, epoch int64) error {
	values := c.Request.Header.Values("X-Upload-Epoch")
	if len(values) == 0 {
		if epoch == 0 {
			return nil
		}
		return contract.ErrUploadContract
	}
	if len(values) != 1 {
		return contract.ErrInvalidInput
	}
	value, err := strconv.ParseInt(strings.TrimSpace(values[0]), 10, 64)
	if err != nil || value < 0 {
		return contract.ErrInvalidInput
	}
	if value != epoch {
		return contract.ErrUploadContract
	}
	return nil
}
