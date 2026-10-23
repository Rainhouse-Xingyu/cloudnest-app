package handler

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"cloudenest_go/internal/blob"
	"cloudenest_go/internal/service/contract"
	"cloudenest_go/model"
	"github.com/gin-gonic/gin"
)

const privateDownloadCache = "private, no-store, no-transform"

func clearDownloadIdentity(h http.Header) {
	for _, key := range []string{"ETag", "Repr-Digest", "Content-Digest", "Last-Modified"} {
		h.Del(key)
	}
}

// storage is authorized metadata for a pinned immutable source. Generated
// archives/thumbnails pass nil and are hashed through the exact served FD.
func (h *FileHandler) sendFileWithRateLimit(c *gin.Context, path, name string, rate int64, storage *model.Storage) error {
	if rate <= 0 {
		return contract.ErrRatePolicy
	}
	var file *os.File
	var digest string
	var err error
	if storage != nil {
		var pinned model.Storage
		file, pinned, err = blob.OpenPinnedStorage(c.Request.Context(), *storage)
		digest = strings.ToLower(pinned.FileHash)
	} else {
		var before os.FileInfo
		before, err = os.Lstat(path)
		if err == nil && !before.Mode().IsRegular() {
			err = contract.ErrStorageIntegrity
		}
		if err == nil {
			file, err = os.Open(path)
		}
		if err == nil {
			var after os.FileInfo
			after, err = file.Stat()
			if err == nil && !os.SameFile(before, after) {
				err = contract.ErrStorageIntegrity
			}
			if err != nil {
				_ = file.Close()
			}
		}
	}
	if err != nil {
		return err
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		return err
	}
	if !stat.Mode().IsRegular() {
		return contract.ErrStorageIntegrity
	}
	if storage == nil {
		hash := sha256.New()
		n, err := io.CopyBuffer(hash, &uploadContextReader{ctx: c.Request.Context(), in: file}, make([]byte, 64<<10))
		if err != nil {
			return err
		}
		if n != stat.Size() {
			return contract.ErrStorageIntegrity
		}
		digest = hex.EncodeToString(hash.Sum(nil))
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return err
		}
	}
	sum, err := hex.DecodeString(digest)
	if err != nil || len(sum) != sha256.Size {
		return contract.ErrStorageIntegrity
	}
	tag := `"sha256-` + digest + `"`
	c.Header("ETag", tag)
	c.Header("Repr-Digest", "sha-256=:"+base64.StdEncoding.EncodeToString(sum)+":")
	c.Header("Cache-Control", privateDownloadCache)
	if c.Writer.Header().Get("Content-Disposition") == "" {
		c.Header("Content-Disposition", `attachment; filename="`+sanitizeFileName(name)+`"`)
	}
	req := c.Request
	if values := req.Header.Values("If-Range"); len(values) > 0 && (len(values) != 1 || strings.TrimSpace(values[0]) != tag) {
		// A filesystem timestamp is not a strong validator for a mutable file URL:
		// different versions can be created within the same second. Restart safely.
		req = req.Clone(req.Context())
		req.Header.Del("Range")
	}
	original := c.Writer
	writer := &pacedResponseWriter{ResponseWriter: original, pacer: transferPacer{ctx: c.Request.Context(), rate: rate, start: time.Now()}}
	c.Writer = writer
	defer func() { c.Writer = original }()
	http.ServeContent(writer, req, sanitizeFileName(name), stat.ModTime(), file)
	return writer.err
}

func (h *FileHandler) sendPreviewJSON(c *gin.Context, rate int64, value any) error {
	c.Header("Cache-Control", privateDownloadCache)
	original := c.Writer
	writer := &pacedResponseWriter{ResponseWriter: original, pacer: transferPacer{ctx: c.Request.Context(), rate: rate, start: time.Now()}}
	c.Writer = writer
	defer func() { c.Writer = original }()
	c.JSON(http.StatusOK, value)
	return writer.err
}
