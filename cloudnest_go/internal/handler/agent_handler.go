package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"cloudenest_go/internal/agentauth"
	"cloudenest_go/internal/middleware"
	"github.com/gin-gonic/gin"
)

type AgentHandler struct{ Identity *agentauth.Manager }

func AgentTransport(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if c.Request.TLS == nil {
		c.AbortWithStatusJSON(http.StatusUpgradeRequired, gin.H{"error": "HTTPS required"})
		return
	}
	c.Next()
}

func agentBody[T any](c *gin.Context, dst *T) bool {
	if c.Request.URL.RawQuery != "" {
		c.JSON(400, gin.H{"error": "invalid request"})
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 4096))
	decoder.DisallowUnknownFields()
	var value *T
	if decoder.Decode(&value) != nil || value == nil || decoder.Decode(&struct{}{}) != io.EOF {
		c.JSON(400, gin.H{"error": "invalid request"})
		return false
	}
	*dst = *value
	return true
}

func ownerDevice(c *gin.Context) (int64, int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(400, gin.H{"error": "invalid device id"})
		return 0, 0, false
	}
	return c.GetInt64(middleware.ContextUserIDKey), id, true
}

func agentError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, agentauth.ErrInput):
		c.JSON(400, gin.H{"error": "invalid request"})
	case errors.Is(err, agentauth.ErrDenied), errors.Is(err, agentauth.ErrReauth):
		c.JSON(401, gin.H{"error": "authentication failed"})
	case errors.Is(err, agentauth.ErrOwner):
		c.JSON(404, gin.H{"error": "device unavailable"})
	default:
		c.JSON(503, gin.H{"error": "device authentication unavailable"})
	}
}

func (h *AgentHandler) Issue(c *gin.Context) {
	uid, id, ok := ownerDevice(c)
	if !ok {
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if !agentBody(c, &req) {
		return
	}
	out, err := h.Identity.Issue(c.Request.Context(), uid, id, req.Password)
	if err != nil {
		agentError(c, err)
		return
	}
	c.JSON(201, out)
}
func (h *AgentHandler) Revoke(c *gin.Context) {
	uid, id, ok := ownerDevice(c)
	if !ok {
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if !agentBody(c, &req) {
		return
	}
	if err := h.Identity.Revoke(c.Request.Context(), uid, id, req.Password); err != nil {
		agentError(c, err)
		return
	}
	c.Status(204)
}
func (h *AgentHandler) Exchange(c *gin.Context) {
	var req struct {
		Enrollment string `json:"enrollment"`
	}
	if !agentBody(c, &req) {
		return
	}
	out, err := h.Identity.Exchange(c.Request.Context(), req.Enrollment)
	if err != nil {
		agentError(c, err)
		return
	}
	c.JSON(201, out)
}
func (h *AgentHandler) Heartbeat(c *gin.Context) {
	var req struct {
		ClientVersion string `json:"client_version"`
	}
	if !agentBody(c, &req) {
		return
	}
	scheme, key, ok := strings.Cut(c.GetHeader("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		agentError(c, agentauth.ErrDenied)
		return
	}
	out, err := h.Identity.Beat(c.Request.Context(), key, req.ClientVersion)
	if err != nil {
		agentError(c, err)
		return
	}
	c.JSON(200, out)
}
