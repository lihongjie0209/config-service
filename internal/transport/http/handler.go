package httptransport

import (
	"encoding/json"
	"log/slog"

	"github.com/gin-gonic/gin"
	"github.com/lihongjie0209/config-service/internal/apperror"
	"github.com/lihongjie0209/config-service/internal/buildinfo"
	configdomain "github.com/lihongjie0209/config-service/internal/dynamicconfig"
	"github.com/lihongjie0209/config-service/internal/health"
)

type Handler struct {
	logger *slog.Logger
	health *health.Service

	configs *configdomain.Service
}

func NewHandler(healthService *health.Service, configService *configdomain.Service, logger *slog.Logger) *Handler {
	return &Handler{health: healthService, configs: configService, logger: logger}
}

type PutConfigRequest struct {
	ID                string          `json:"id"`
	Environment       string          `json:"environment" binding:"required"`
	TenantID          string          `json:"tenant_id"`
	Service           string          `json:"service" binding:"required"`
	Key               string          `json:"key" binding:"required"`
	Value             json.RawMessage `json:"value" swaggertype:"object"`
	SecretRef         string          `json:"secret_ref"`
	RolloutPercentage int32           `json:"rollout_percentage"`
	ExpectedVersion   int64           `json:"expected_version"`
}
type ConfigIDRequest struct {
	ID              string `json:"id" binding:"required"`
	ExpectedVersion int64  `json:"expected_version" binding:"required"`
}
type RollbackConfigRequest struct {
	ID              string `json:"id" binding:"required"`
	TargetRevision  int64  `json:"target_revision" binding:"required"`
	ExpectedVersion int64  `json:"expected_version" binding:"required"`
}
type ResolveConfigRequest struct {
	Environment string   `json:"environment" binding:"required"`
	TenantID    string   `json:"tenant_id"`
	Service     string   `json:"service" binding:"required"`
	Keys        []string `json:"keys" binding:"required"`
	SubjectID   string   `json:"subject_id"`
}
type ListConfigRequest struct {
	Environment string `json:"environment" binding:"required"`
	TenantID    string `json:"tenant_id"`
	Service     string `json:"service" binding:"required"`
	Page        int    `json:"page"`
	PageSize    int    `json:"page_size"`
}

type MeResponseBody struct {
	Subject string `json:"subject"`
}

// Login godoc
// @Summary Issue a JWT access token
// @Tags authentication
// @Accept json
// @Produce json
// @Param request body LoginRequest true "Client credentials"
// @Success 200 {object} Response{body=LoginResponseBody}
// @Failure 400 {object} Response "Code 10001: invalid request"
// @Failure 401 {object} Response "Code 20001: invalid credentials"
// @Failure 429 {object} Response "Code 10029: rate limited"

// Live godoc
// @Summary Check process liveness
// @Tags operations
// @Produce json
// @Success 200 {object} Response{body=health.Status}
// @Router /live [post]
func (h *Handler) Live(c *gin.Context) { OK(c, h.health.Live()) }

// Ready godoc
// @Summary Check database and Redis readiness
// @Tags operations
// @Produce json
// @Success 200 {object} Response{body=health.Status}
// @Failure 503 {object} Response{body=health.Status} "Code 50003: dependency unavailable"
// @Router /ready [post]
func (h *Handler) Ready(c *gin.Context) {
	status, ready := h.health.Ready(c.Request.Context())
	if !ready {
		c.JSON(503, Response{Code: apperror.CodeDependencyUnavailable, Message: "service is not ready", Body: status, RequestID: requestID(c)})
		return
	}
	OK(c, status)
}

// Me godoc
// @Summary Return the authenticated subject
// @Tags authentication
// @Produce json
// @Security Bearer
// @Success 200 {object} Response{body=MeResponseBody}
// @Failure 401 {object} Response "Code 20001: unauthorized"
// @Router /api/v1/me [post]
func (h *Handler) Me(c *gin.Context) {
	subject, _ := c.Get("subject")
	OK(c, gin.H{"subject": subject})
}

// Version godoc
// @Summary Return build and runtime version information
// @Tags operations
// @Produce json
// @Success 200 {object} Response{body=buildinfo.Info}
// @Router /api/v1/version [post]
func (h *Handler) Version(c *gin.Context) { OK(c, buildinfo.Current()) }

// PutConfig godoc
// @Summary Create or update a configuration draft
// @Tags configuration
// @Security Bearer
// @Param request body PutConfigRequest true "Configuration draft"
// @Success 200 {object} Response{body=dynamicconfig.Entry}
// @Router /api/v1/config/entries/put-draft [post]
func (h *Handler) PutConfig(c *gin.Context) {
	var request PutConfigRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		Fail(c, h.logger, apperror.Invalid("invalid json request", err))
		return
	}
	value, err := h.configs.PutDraft(c.Request.Context(), configdomain.Entry{ID: request.ID, Environment: request.Environment, TenantID: request.TenantID, Service: request.Service, Key: request.Key, Value: request.Value, SecretRef: request.SecretRef, RolloutPercentage: request.RolloutPercentage}, request.ExpectedVersion)
	if err != nil {
		Fail(c, h.logger, err)
		return
	}
	OK(c, value)
}

// PublishConfig godoc
// @Summary Publish a configuration revision
// @Tags configuration
// @Security Bearer
// @Param request body ConfigIDRequest true "Configuration version"
// @Success 200 {object} Response{body=dynamicconfig.Entry}
// @Router /api/v1/config/entries/publish [post]
func (h *Handler) PublishConfig(c *gin.Context) {
	var request ConfigIDRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		Fail(c, h.logger, apperror.Invalid("invalid json request", err))
		return
	}
	value, err := h.configs.Publish(c.Request.Context(), request.ID, request.ExpectedVersion)
	if err != nil {
		Fail(c, h.logger, err)
		return
	}
	OK(c, value)
}

// RollbackConfig godoc
// @Summary Roll back a published configuration
// @Tags configuration
// @Security Bearer
// @Param request body RollbackConfigRequest true "Rollback target"
// @Success 200 {object} Response{body=dynamicconfig.Entry}
// @Router /api/v1/config/entries/rollback [post]
func (h *Handler) RollbackConfig(c *gin.Context) {
	var request RollbackConfigRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		Fail(c, h.logger, apperror.Invalid("invalid json request", err))
		return
	}
	value, err := h.configs.Rollback(c.Request.Context(), request.ID, request.TargetRevision, request.ExpectedVersion)
	if err != nil {
		Fail(c, h.logger, err)
		return
	}
	OK(c, value)
}

// ResolveConfig godoc
// @Summary Resolve effective configuration for a subject
// @Tags configuration
// @Security Bearer
// @Param request body ResolveConfigRequest true "Resolution scope"
// @Success 200 {object} Response
// @Router /api/v1/config/resolve [post]
func (h *Handler) ResolveConfig(c *gin.Context) {
	var request ResolveConfigRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		Fail(c, h.logger, apperror.Invalid("invalid json request", err))
		return
	}
	values, etag, err := h.configs.Resolve(c.Request.Context(), request.Environment, request.TenantID, request.Service, request.SubjectID, request.Keys)
	if err != nil {
		Fail(c, h.logger, err)
		return
	}
	OK(c, gin.H{"entries": values, "etag": etag})
}

// ListConfig godoc
// @Summary List configuration entries
// @Tags configuration
// @Security Bearer
// @Param request body ListConfigRequest true "Configuration scope"
// @Success 200 {object} Response{body=dynamicconfig.Page}
// @Router /api/v1/config/entries/list [post]
func (h *Handler) ListConfig(c *gin.Context) {
	var request ListConfigRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		Fail(c, h.logger, apperror.Invalid("invalid json request", err))
		return
	}
	value, err := h.configs.List(c.Request.Context(), request.Environment, request.TenantID, request.Service, request.Page, request.PageSize)
	if err != nil {
		Fail(c, h.logger, err)
		return
	}
	OK(c, value)
}

// CreateUser godoc
// @Summary Create a user
// @Tags users
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body CreateUserRequest true "User"
// @Success 200 {object} Response{body=user.User}
// @Failure 400 {object} Response "Code 10001: invalid request"
// @Failure 409 {object} Response "Code 30009: email already exists"

// GetUser godoc
// @Summary Get a user
// @Tags users
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body GetUserRequest true "User ID"
// @Success 200 {object} Response{body=user.User}
// @Failure 404 {object} Response "Code 10004: user not found"

// ListUsers godoc
// @Summary List users
// @Tags users
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body ListUsersRequest true "Pagination"
// @Success 200 {object} Response{body=user.Page}

// UpdateUser godoc
// @Summary Update a user using optimistic locking
// @Tags users
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body UpdateUserRequest true "User and current version"
// @Success 200 {object} Response{body=user.User}
// @Failure 409 {object} Response "Code 30009: version conflict"

// DeleteUser godoc
// @Summary Delete a user using optimistic locking
// @Tags users
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body DeleteUserRequest true "User ID and current version"
// @Success 200 {object} Response
