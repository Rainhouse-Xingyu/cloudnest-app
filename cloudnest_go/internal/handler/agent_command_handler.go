package handler

import (
	"errors"
	"net/http"
	"strings"

	"cloudenest_go/internal/agentauth"
	"cloudenest_go/internal/agentcommand"
	"cloudenest_go/internal/agentstatus"
	"cloudenest_go/internal/frpconfig"
	"cloudenest_go/internal/middleware"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type AgentCommandHandler struct{ Commands *agentcommand.Manager }

func commandError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, agentcommand.ErrInput), errors.Is(err, agentauth.ErrInput):
		c.JSON(400, gin.H{"error": "invalid command request"})
	case errors.Is(err, agentauth.ErrDenied):
		c.JSON(401, gin.H{"error": "invalid device credentials"})
	case errors.Is(err, agentauth.ErrOwner), errors.Is(err, gorm.ErrRecordNotFound):
		c.JSON(404, gin.H{"error": "resource unavailable"})
	case errors.Is(err, agentcommand.ErrConflict), errors.Is(err, agentcommand.ErrUnregistered):
		c.JSON(409, gin.H{"error": "command state conflict or registration required"})
	case errors.Is(err, agentcommand.ErrFull):
		c.JSON(429, gin.H{"error": "device command queue full"})
	case errors.Is(err, frpconfig.ErrInvalid), errors.Is(err, frpconfig.ErrLegacy), errors.Is(err, frpconfig.ErrUnavailable):
		c.JSON(503, gin.H{"error": "configuration unavailable"})
	default:
		c.JSON(503, gin.H{"error": "command service unavailable"})
	}
}
func commandKey(c *gin.Context) string {
	scheme, key, ok := strings.Cut(c.GetHeader("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return key
}

func (h *AgentCommandHandler) Enqueue(c *gin.Context) {
	uid, device, ok := ownerDevice(c)
	if !ok {
		return
	}
	var req agentcommand.EnqueueInput
	if !agentBody(c, &req) {
		return
	}
	row, err := h.Commands.Enqueue(c.Request.Context(), uid, c.GetBool(middleware.ContextIsAdminKey), device, req, c.ClientIP())
	if err != nil {
		commandError(c, err)
		return
	}
	c.JSON(200, gin.H{"item": row, "command_id": row.ID})
}

// Existing management routes now produce durable commands too. Request IDs
// must be retained by clients and reused only for an identical operation.
func (h *AgentCommandHandler) Deploy(c *gin.Context) { h.compatEnqueue(c, "deploy_config") }
func (h *AgentCommandHandler) Reload(c *gin.Context) { h.compatEnqueue(c, "reload") }
func (h *AgentCommandHandler) compatEnqueue(c *gin.Context, action string) {
	uid, device, ok := ownerDevice(c)
	if !ok {
		return
	}
	var body DeployConfigRequest
	if !agentBody(c, &body) {
		return
	}
	req := agentcommand.EnqueueInput{RequestID: c.GetHeader("Idempotency-Key"), Action: action}
	if body.ConfigID != nil {
		req.ConfigID = *body.ConfigID
	}
	row, err := h.Commands.Enqueue(c.Request.Context(), uid, c.GetBool(middleware.ContextIsAdminKey), device, req, c.ClientIP())
	if err != nil {
		commandError(c, err)
		return
	}
	c.JSON(200, gin.H{"command_id": row.ID, "device_id": row.DeviceID, "config_id": row.ConfigID, "message": "command queued"})
}
func (h *AgentCommandHandler) List(c *gin.Context) {
	uid, device, ok := ownerDevice(c)
	if !ok {
		return
	}
	limit, err := parseLimitWithDefault(c.Query("limit"), 20, 100)
	if err != nil {
		commandError(c, agentcommand.ErrInput)
		return
	}
	rows, err := h.Commands.List(c.Request.Context(), uid, c.GetBool(middleware.ContextIsAdminKey), device, limit)
	if err != nil {
		commandError(c, err)
		return
	}
	c.JSON(200, gin.H{"items": rows})
}
func (h *AgentCommandHandler) Claim(c *gin.Context) {
	var body struct {
		ClaimID string `json:"claim_id"`
	}
	if !agentBody(c, &body) {
		return
	}
	out, err := h.Commands.Claim(c.Request.Context(), commandKey(c), body.ClaimID)
	if err != nil {
		commandError(c, err)
		return
	}
	if out == nil {
		c.Status(http.StatusNoContent)
		return
	}
	c.JSON(200, out)
}
func (h *AgentCommandHandler) Renew(c *gin.Context) {
	var body struct {
		LeaseID string `json:"lease_id"`
	}
	if !agentBody(c, &body) {
		return
	}
	until, err := h.Commands.Renew(c.Request.Context(), commandKey(c), c.Param("id"), body.LeaseID)
	if err != nil {
		commandError(c, err)
		return
	}
	c.JSON(200, gin.H{"lease_expires_at": until})
}
func (h *AgentCommandHandler) Configuration(c *gin.Context) {
	var body struct {
		LeaseID string `json:"lease_id"`
	}
	if !agentBody(c, &body) {
		return
	}
	out, err := h.Commands.Configuration(c.Request.Context(), commandKey(c), c.Param("id"), body.LeaseID)
	if err != nil {
		commandError(c, err)
		return
	}
	c.JSON(200, out)
}
func (h *AgentCommandHandler) Finish(c *gin.Context) {
	var body struct {
		LeaseID    string              `json:"lease_id"`
		Success    *bool               `json:"success"`
		Code       string              `json:"code"`
		Diagnostic *agentstatus.Report `json:"diagnostic"`
	}
	if !agentBody(c, &body) {
		return
	}
	if body.Success == nil {
		commandError(c, agentcommand.ErrInput)
		return
	}
	err := h.Commands.FinishWithReport(c.Request.Context(), commandKey(c), c.Param("id"), body.LeaseID, *body.Success, body.Code, body.Diagnostic)
	if err != nil {
		commandError(c, err)
		return
	}
	c.Status(204)
}
