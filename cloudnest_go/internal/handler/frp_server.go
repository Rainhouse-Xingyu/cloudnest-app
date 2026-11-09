package handler

import (
	"errors"
	"strconv"

	"cloudenest_go/internal/frpconfig"
	"cloudenest_go/internal/middleware"
	"cloudenest_go/internal/service/contract"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type frpServerRequest struct {
	Name               string  `json:"name"`
	Address            string  `json:"server_addr"`
	Port               int     `json:"server_port"`
	Token              *string `json:"token"`
	TCPStart           int     `json:"tcp_start"`
	TCPEnd             int     `json:"tcp_end"`
	HTTPPort           int     `json:"http_port"`
	HTTPSPort          int     `json:"https_port"`
	Enabled            *bool   `json:"enabled"`
	ExpectedRevisionID int64   `json:"expected_revision_id"`
}

func (h *FrpHandler) serverActor(c *gin.Context) contract.FrpActor {
	return contract.FrpActor{UserID: c.GetInt64(middleware.ContextUserIDKey), Admin: h.isAdmin(c), ClientIP: c.ClientIP()}
}
func (h *FrpHandler) WriteServer(c *gin.Context) {
	id := int64(0)
	var err error
	if c.Param("id") != "" {
		id, err = strconv.ParseInt(c.Param("id"), 10, 64)
		if err != nil || id <= 0 {
			c.JSON(400, gin.H{"error": "invalid server id"})
			return
		}
	}
	var req frpServerRequest
	if !agentBody(c, &req) {
		return
	}
	view, err := h.Svc.WriteServer(c.Request.Context(), h.serverActor(c), id, contract.FrpServerInput{Spec: frpconfig.ServerSpec{Name: req.Name, Address: req.Address, Port: req.Port, TCPStart: req.TCPStart, TCPEnd: req.TCPEnd, HTTPPort: req.HTTPPort, HTTPSPort: req.HTTPSPort}, Token: req.Token, Enabled: req.Enabled, ExpectedRevisionID: req.ExpectedRevisionID})
	if err != nil {
		h.writeServerError(c, err)
		return
	}
	c.JSON(200, gin.H{"item": view})
}
func (h *FrpHandler) GetServer(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(400, gin.H{"error": "invalid server id"})
		return
	}
	view, err := h.Svc.GetServer(c.Request.Context(), h.serverActor(c), id)
	if err != nil {
		h.writeServerError(c, err)
		return
	}
	c.JSON(200, gin.H{"item": view})
}
func (h *FrpHandler) ListServers(c *gin.Context) {
	after, err := strconv.ParseInt(c.DefaultQuery("after_id", "0"), 10, 64)
	if err != nil {
		c.JSON(400, gin.H{"error": "invalid cursor"})
		return
	}
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if err != nil {
		c.JSON(400, gin.H{"error": "invalid limit"})
		return
	}
	rows, err := h.Svc.ListServers(c.Request.Context(), h.serverActor(c), after, limit)
	if err != nil {
		h.writeServerError(c, err)
		return
	}
	next := int64(0)
	if len(rows) > 0 {
		next = rows[len(rows)-1].ID
	}
	c.JSON(200, gin.H{"items": rows, "next_after_id": next})
}
func (h *FrpHandler) writeServerError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, contract.ErrInvalidInput):
		c.JSON(400, gin.H{"error": "invalid server policy"})
	case errors.Is(err, contract.ErrConflict), errors.Is(err, contract.ErrFrpDeviceDisabled):
		c.JSON(409, gin.H{"error": "server endpoint, revision or mapping policy conflict"})
	case errors.Is(err, contract.ErrForbidden):
		c.JSON(403, gin.H{"error": "permission denied"})
	case errors.Is(err, gorm.ErrRecordNotFound):
		c.JSON(404, gin.H{"error": "server or device not found"})
	case errors.Is(err, frpconfig.ErrUnavailable), errors.Is(err, frpconfig.ErrInvalid), errors.Is(err, contract.ErrFrpMappingBusy):
		c.JSON(503, gin.H{"error": "server configuration unavailable"})
	default:
		c.JSON(500, gin.H{"error": "server operation failed"})
	}
}
