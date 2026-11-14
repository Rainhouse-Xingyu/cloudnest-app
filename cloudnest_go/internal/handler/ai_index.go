package handler

import (
	"cloudenest_go/internal/middleware"
	"cloudenest_go/internal/service/contract"
	"context"
	"errors"
	"github.com/gin-gonic/gin"
	"net/http"
	"strconv"
	"time"
)

func indexError(c *gin.Context, err error) {
	code, message := http.StatusServiceUnavailable, "index service unavailable"
	switch {
	case errors.Is(err, contract.ErrInvalidInput):
		code, message = http.StatusBadRequest, "invalid index request"
	case errors.Is(err, contract.ErrNotFound):
		code, message = http.StatusNotFound, "file not found"
	case errors.Is(err, contract.ErrConflict):
		code, message = http.StatusConflict, "index changed; refresh status before rebuilding"
	}
	c.JSON(code, gin.H{"error": message})
}

func (h *AIHandler) ListIndex(c *gin.Context) {
	var q struct {
		After int64 `form:"after_id"`
		Limit int   `form:"limit"`
	}
	q.Limit = 50
	if err := c.ShouldBindQuery(&q); err != nil {
		indexError(c, contract.ErrInvalidInput)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	rows, more, err := h.Svc.ListIndex(ctx, c.GetInt64(middleware.ContextUserIDKey), q.After, q.Limit)
	if err != nil {
		indexError(c, err)
		return
	}
	next := q.After
	if len(rows) > 0 {
		next = rows[len(rows)-1].FileID
	}
	c.JSON(http.StatusOK, gin.H{"items": rows, "next_after_id": next, "has_more": more})
}

func (h *AIHandler) IndexStatus(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		indexError(c, contract.ErrInvalidInput)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	row, err := h.Svc.IndexStatus(ctx, c.GetInt64(middleware.ContextUserIDKey), id)
	if err != nil {
		indexError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"item": row})
}

func (h *AIHandler) RebuildIndex(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16<<10)
	var req struct {
		Items []contract.AIRebuildItem `json:"items"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		indexError(c, contract.ErrInvalidInput)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	rows, err := h.Svc.RebuildIndex(ctx, c.GetInt64(middleware.ContextUserIDKey), req.Items, c.ClientIP())
	if err != nil {
		indexError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"items": rows, "status": "queued"})
}
