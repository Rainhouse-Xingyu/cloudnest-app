package handler

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"cloudenest_go/internal/middleware"
	"cloudenest_go/internal/service"
	"cloudenest_go/internal/service/contract"
	"cloudenest_go/internal/transfer"
	"cloudenest_go/model"
	"github.com/gin-gonic/gin"
)

// UploadChunk requires metadata before the single file part, as sent by the
// frozen Swift client. It never asks net/http to buffer/spool a whole multipart.
func (h *FileHandler) UploadChunk(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)
	budget := &chunkBodyReader{ReadCloser: c.Request.Body, limit: 64 * 1024}
	c.Request.Body = budget
	reader, err := c.Request.MultipartReader()
	if err != nil {
		writeChunkError(c, contract.ErrInvalidInput)
		return
	}
	fields := map[string]string{}
	var task model.UploadTask
	var index int
	var expected int64
	for {
		part, err := reader.NextPart()
		if err != nil {
			writeChunkError(c, chunkReadError(err))
			return
		}
		if part.FormName() == "chunk" && part.FileName() != "" {
			if len(fields) != 3 {
				writeChunkError(c, contract.ErrInvalidInput)
				return
			}
			h.acceptChunkPart(c, reader, part, task, index, expected, strings.ToLower(fields["chunk_md5"]))
			return
		}
		name := part.FormName()
		if part.FileName() != "" || (name != "upload_id" && name != "chunk_index" && name != "chunk_md5") {
			writeChunkError(c, contract.ErrInvalidInput)
			return
		}
		if _, exists := fields[name]; exists {
			writeChunkError(c, contract.ErrInvalidInput)
			return
		}
		value, err := io.ReadAll(io.LimitReader(part, 1025))
		if err != nil {
			writeChunkError(c, err)
			return
		}
		if len(value) > 1024 {
			writeChunkError(c, contract.ErrInvalidInput)
			return
		}
		fields[name] = strings.TrimSpace(string(value))
		if fields[name] == "" {
			writeChunkError(c, contract.ErrInvalidInput)
			return
		}
		if len(fields) == 3 {
			index, err = strconv.Atoi(fields["chunk_index"])
			if err != nil || index < 0 {
				writeChunkError(c, contract.ErrInvalidInput)
				return
			}
			digest, err := hex.DecodeString(fields["chunk_md5"])
			if err != nil || len(digest) != 16 {
				writeChunkError(c, contract.ErrInvalidInput)
				return
			}
			task, err = h.Svc.ValidateUploadChunk(c.Request.Context(), uid, fields["upload_id"], index)
			if err != nil {
				writeChunkError(c, err)
				return
			}
			expected, err = contract.ExpectedChunkBytes(task, index)
			if err != nil {
				writeChunkError(c, err)
				return
			}
			if c.Request.ContentLength > expected+64*1024 {
				writeChunkError(c, &http.MaxBytesError{Limit: expected + 64*1024})
				return
			}
			budget.limit = expected + 64*1024
		}
	}
}

// A metadata/header budget applies even before the task is known; after its
// third field, the same reader admits only the exact chunk plus bounded framing.
type chunkBodyReader struct {
	io.ReadCloser
	count, limit int64
}

func (r *chunkBodyReader) Read(p []byte) (int, error) {
	remaining := r.limit - r.count + 1
	if remaining <= 0 {
		return 0, &http.MaxBytesError{Limit: r.limit}
	}
	if int64(len(p)) > remaining {
		p = p[:remaining]
	}
	n, err := r.ReadCloser.Read(p)
	r.count += int64(n)
	if r.count > r.limit {
		return n, &http.MaxBytesError{Limit: r.limit}
	}
	return n, err
}
func chunkReadError(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return contract.ErrInvalidInput
	}
	return err
}
func writeChunkError(c *gin.Context, err error) {
	var tooLarge *http.MaxBytesError
	var network net.Error
	switch {
	case errors.As(err, &tooLarge):
		c.JSON(413, gin.H{"error": "upload body too large"})
	case errors.Is(err, multipart.ErrMessageTooLarge):
		c.JSON(413, gin.H{"error": "multipart headers too large"})
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &network) && network.Timeout()):
		c.JSON(408, gin.H{"error": "upload body deadline exceeded"})
	case errors.Is(err, contract.ErrInvalidInput), errors.Is(err, service.ErrChunkIndexOutOfRange):
		c.JSON(400, gin.H{"error": "invalid chunk parameters, length or digest"})
	case errors.Is(err, service.ErrUploadTaskCompleted):
		c.JSON(409, gin.H{"error": "upload task already completed"})
	case errors.Is(err, service.ErrUploadTaskNotFound):
		c.JSON(404, gin.H{"error": "upload task not found"})
	default:
		writeFileMutationError(c, err, "chunk upload failed")
	}
}
func (h *FileHandler) acceptChunkPart(c *gin.Context, reader *multipart.Reader, part *multipart.Part, task model.UploadTask, index int, expected int64, md5sum string) {
	candidate, err := h.Transfers.Create(c.Request.Context(), *task.UserID, task.ID, "chunk", expected+1)
	if err != nil {
		writeChunkError(c, err)
		return
	}
	disposable := true
	defer func() {
		if err := candidate.Close(disposable); err != nil {
			h.recordUploadCleanupFailure(context.WithoutCancel(c.Request.Context()), *task.UserID, nil, "upload_chunk_candidate_cleanup", c.ClientIP())
		}
	}()
	sha, md := sha256.New(), md5.New()
	total, err := copyUploadPart(candidate.Context(), io.MultiWriter(candidate, sha, md), io.LimitReader(part, expected+1), h.getUploadRateBytesPerSec(c))
	if err != nil {
		writeChunkError(c, err)
		return
	}
	if total != expected || hex.EncodeToString(md.Sum(nil)) != md5sum {
		writeChunkError(c, contract.ErrInvalidInput)
		return
	}
	// No second file, duplicate metadata or trailing part may be silently ignored.
	if _, err := reader.NextPart(); err != io.EOF {
		if err == nil {
			err = contract.ErrInvalidInput
		} else {
			err = chunkReadError(err)
		}
		writeChunkError(c, err)
		return
	}
	// Consume the bounded HTTP envelope to EOF as well as the MIME boundary.
	// An endless epilogue cannot outlive admission; complete valid requests can
	// retain their connection without timing out net/http's background reader.
	if _, err := io.Copy(io.Discard, c.Request.Body); err != nil {
		writeChunkError(c, err)
		return
	}
	if err := candidate.Seal(total, hex.EncodeToString(sha.Sum(nil))); err != nil {
		writeChunkError(c, err)
		return
	}
	commit, err := h.Svc.MarkChunkUploaded(candidate.Context(), task, task.UploadID, index, total, candidate.Path(), candidate.Root(), hex.EncodeToString(sha.Sum(nil)), md5sum, candidate.Token())
	disposable = commit.CandidateDisposable
	if err != nil {
		writeChunkError(c, err)
		return
	}
	c.JSON(200, gin.H{"message": "chunk uploaded"})
}
func copyUploadPart(ctx context.Context, dst io.Writer, src io.Reader, rate int64) (int64, error) {
	buffer := make([]byte, 32*1024)
	start := time.Now()
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		n, readErr := src.Read(buffer)
		if n > 0 {
			written, err := dst.Write(buffer[:n])
			total += int64(written)
			if err != nil {
				return total, err
			}
			if written != n {
				return total, io.ErrShortWrite
			}
			if rate > 0 {
				wait := time.Duration(float64(total)/float64(rate)*float64(time.Second)) - time.Since(start)
				if wait > 0 {
					timer := time.NewTimer(wait)
					select {
					case <-ctx.Done():
						timer.Stop()
						return total, ctx.Err()
					case <-timer.C:
					}
				}
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				return total, nil
			}
			return total, chunkReadError(readErr)
		}
	}
}

func (h *FileHandler) cleanupChunkManifest(ctx context.Context, uid int64, chunks []model.UploadChunk, clientIP string) {
	cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer stop()
	_, err := h.Transfers.Reconcile(cleanup)
	if err != nil && !errors.Is(err, transfer.ErrPending) {
		h.recordUploadCleanupFailure(cleanup, uid, nil, "upload_chunk_cleanup", clientIP)
	}
}
