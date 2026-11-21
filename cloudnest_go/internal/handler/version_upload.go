package handler

import (
	"cloudenest_go/internal/middleware"
	"cloudenest_go/internal/service"
	"cloudenest_go/internal/service/contract"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"io"
	"net/http"
	"strconv"
)

func (h *FileHandler) InitVersionUpload(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16<<10)
	fileID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	keys := c.Request.Header.Values("Idempotency-Key")
	if err != nil || fileID <= 0 || len(keys) != 1 || keys[0] == "" {
		c.JSON(400, gin.H{"error": "invalid version upload parameters"})
		return
	}
	var input struct {
		Filename    string `json:"filename"`
		Size        *int64 `json:"total_size"`
		ChunkSize   int    `json:"chunk_size"`
		TotalChunks int    `json:"total_chunks"`
		BaseVersion int    `json:"base_version"`
		Comment     string `json:"comment"`
	}
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	var trailing any
	if err := decoder.Decode(&input); err != nil || input.Size == nil {
		c.JSON(400, gin.H{"error": "invalid version upload request"})
		return
	}
	if decoder.Decode(&trailing) != io.EOF {
		c.JSON(400, gin.H{"error": "invalid version upload request"})
		return
	}
	if *input.Size < 0 || *input.Size > contract.MaxUploadFileBytes {
		c.JSON(400, gin.H{"error": "invalid version upload size"})
		return
	}
	result, err := h.Svc.InitUpload(c.Request.Context(), c.GetInt64(middleware.ContextUserIDKey), service.FileUploadInitInput{
		RequestKey: keys[0], Filename: input.Filename, TotalSize: *input.Size, ChunkSize: input.ChunkSize, TotalChunks: input.TotalChunks,
		Version: &contract.VersionUpload{FileID: fileID, BaseVersion: input.BaseVersion, Comment: input.Comment},
	})
	if err != nil {
		writeFileMutationError(c, err, "version upload rejected")
		return
	}
	c.JSON(200, versionUploadFields(gin.H{"upload_id": result.UploadID, "transfer_epoch": result.TransferEpoch, "uploaded_chunks": result.UploadedChunks, "uploaded_count": result.UploadedCount, "status": result.Status}, result.Version))
}

func versionUploadFields(fields gin.H, binding *contract.VersionUpload) gin.H {
	if binding != nil {
		fields["kind"] = "file_version"
		fields["file_id"] = binding.FileID
		fields["base_version"] = binding.BaseVersion
	}
	return fields
}
