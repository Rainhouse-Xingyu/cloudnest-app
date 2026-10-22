package handler

import (
	"cloudenest_go/internal/service/contract"
	"errors"
	"net/http"

	"cloudenest_go/internal/middleware"
	"cloudenest_go/internal/service"

	"github.com/gin-gonic/gin"
)

// StorageHandler 提供容量概览接口。
type StorageHandler struct {
	Svc *service.StorageService
}

// GetOverview 返回当前用户容量概览（已用/总量/百分比）。
func (h *StorageHandler) GetOverview(c *gin.Context) {
	uid := c.GetInt64(middleware.ContextUserIDKey)

	overview, err := h.Svc.GetOverview(c.Request.Context(), uid)
	if err != nil {
		if errors.Is(err, contract.ErrQuotaAccounting) {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "storage accounting unavailable"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "query storage overview failed"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"item": overview})
}
