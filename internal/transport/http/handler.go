package httptransport

import (
	"encoding/json"
	"log/slog"
	"time"

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
	ApplicationID     string          `json:"application_id"`
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
type ReviewConfigRequest struct {
	ID              string `json:"id" binding:"required"`
	ExpectedVersion int64  `json:"expected_version" binding:"required"`
	Comment         string `json:"comment"`
}
type RollbackConfigRequest struct {
	ID              string `json:"id" binding:"required"`
	TargetRevision  int64  `json:"target_revision" binding:"required"`
	ExpectedVersion int64  `json:"expected_version" binding:"required"`
}
type ResolveConfigRequest struct {
	Environment   string   `json:"environment" binding:"required"`
	TenantID      string   `json:"tenant_id"`
	ApplicationID string   `json:"application_id"`
	Service       string   `json:"service" binding:"required"`
	Keys          []string `json:"keys" binding:"required"`
	SubjectID     string   `json:"subject_id"`
}
type ListConfigRequest struct {
	Environment   string `json:"environment" binding:"required"`
	TenantID      string `json:"tenant_id"`
	ApplicationID string `json:"application_id"`
	Service       string `json:"service" binding:"required"`
	Page          int    `json:"page"`
	PageSize      int    `json:"page_size"`
}

type ConfigEntryResponseBody struct {
	ID                string     `json:"id"`
	Environment       string     `json:"environment"`
	TenantID          string     `json:"tenant_id"`
	ApplicationID     string     `json:"application_id"`
	Service           string     `json:"service"`
	Key               string     `json:"key"`
	Value             any        `json:"value,omitempty"`
	SecretRef         string     `json:"secret_ref"`
	Status            string     `json:"status"`
	Revision          int64      `json:"revision"`
	RolloutPercentage int32      `json:"rollout_percentage"`
	PublishedRevision int64      `json:"published_revision"`
	ReviewComment     string     `json:"review_comment"`
	ReviewedBy        string     `json:"reviewed_by"`
	ReviewedAt        *time.Time `json:"reviewed_at,omitempty"`
	Version           int64      `json:"version"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	CreatedBy         string     `json:"created_by"`
	UpdatedBy         string     `json:"updated_by"`
}

type ConfigPageResponseBody struct {
	Entries  []ConfigEntryResponseBody `json:"entries"`
	Total    int64                     `json:"total"`
	Page     int                       `json:"page"`
	PageSize int                       `json:"page_size"`
}

type ResolveConfigResponseBody struct {
	Entries []ConfigEntryResponseBody `json:"entries"`
	ETag    string                    `json:"etag"`
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
// @Success 200 {object} Response{body=ConfigEntryResponseBody}
// @Router /api/v1/config/entries/put-draft [post]
func (h *Handler) PutConfig(c *gin.Context) {
	var request PutConfigRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		Fail(c, h.logger, apperror.Invalid("invalid json request", err))
		return
	}
	value, err := h.configs.PutDraft(c.Request.Context(), configdomain.Entry{ID: request.ID, Environment: request.Environment, TenantID: request.TenantID, ApplicationID: request.ApplicationID, Service: request.Service, Key: request.Key, Value: request.Value, SecretRef: request.SecretRef, RolloutPercentage: request.RolloutPercentage}, request.ExpectedVersion)
	if err != nil {
		Fail(c, h.logger, err)
		return
	}
	OK(c, configEntryResponse(value))
}

// PublishConfig godoc
// @Summary Publish a configuration revision
// @Tags configuration
// @Security Bearer
// @Param request body ConfigIDRequest true "Configuration version"
// @Success 200 {object} Response{body=ConfigEntryResponseBody}
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
	OK(c, configEntryResponse(value))
}

// SubmitConfig godoc
// @Summary Submit a configuration revision for independent approval
// @Tags configuration
// @Security Bearer
// @Param request body ConfigIDRequest true "Configuration version"
// @Success 200 {object} Response{body=ConfigEntryResponseBody}
// @Router /api/v1/config/entries/submit [post]
func (h *Handler) SubmitConfig(c *gin.Context) {
	var request ConfigIDRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		Fail(c, h.logger, apperror.Invalid("invalid json request", err))
		return
	}
	value, err := h.configs.SubmitForApproval(c.Request.Context(), request.ID, request.ExpectedVersion)
	if err != nil {
		Fail(c, h.logger, err)
		return
	}
	OK(c, configEntryResponse(value))
}

// ApproveConfig godoc
// @Summary Approve a submitted configuration revision
// @Tags configuration
// @Security Bearer
// @Param request body ReviewConfigRequest true "Approval decision"
// @Success 200 {object} Response{body=ConfigEntryResponseBody}
// @Router /api/v1/config/entries/approve [post]
func (h *Handler) ApproveConfig(c *gin.Context) { h.reviewConfig(c, true) }

// RejectConfig godoc
// @Summary Reject a submitted configuration revision
// @Tags configuration
// @Security Bearer
// @Param request body ReviewConfigRequest true "Rejection decision; comment is required"
// @Success 200 {object} Response{body=ConfigEntryResponseBody}
// @Router /api/v1/config/entries/reject [post]
func (h *Handler) RejectConfig(c *gin.Context) { h.reviewConfig(c, false) }

func (h *Handler) reviewConfig(c *gin.Context, approve bool) {
	var request ReviewConfigRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		Fail(c, h.logger, apperror.Invalid("invalid json request", err))
		return
	}
	var (
		value configdomain.Entry
		err   error
	)
	if approve {
		value, err = h.configs.Approve(c.Request.Context(), request.ID, request.ExpectedVersion, request.Comment)
	} else {
		value, err = h.configs.Reject(c.Request.Context(), request.ID, request.ExpectedVersion, request.Comment)
	}
	if err != nil {
		Fail(c, h.logger, err)
		return
	}
	OK(c, configEntryResponse(value))
}

// RollbackConfig godoc
// @Summary Roll back a published configuration
// @Tags configuration
// @Security Bearer
// @Param request body RollbackConfigRequest true "Rollback target"
// @Success 200 {object} Response{body=ConfigEntryResponseBody}
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
	OK(c, configEntryResponse(value))
}

// ResolveConfig godoc
// @Summary Resolve effective configuration for a subject
// @Tags configuration
// @Security Bearer
// @Param request body ResolveConfigRequest true "Resolution scope"
// @Success 200 {object} Response{body=ResolveConfigResponseBody}
// @Router /api/v1/config/resolve [post]
func (h *Handler) ResolveConfig(c *gin.Context) {
	var request ResolveConfigRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		Fail(c, h.logger, apperror.Invalid("invalid json request", err))
		return
	}
	values, etag, err := h.configs.Resolve(c.Request.Context(), request.Environment, request.TenantID, request.ApplicationID, request.Service, request.SubjectID, request.Keys)
	if err != nil {
		Fail(c, h.logger, err)
		return
	}
	OK(c, ResolveConfigResponseBody{Entries: configEntryResponses(values), ETag: etag})
}

// ListConfig godoc
// @Summary List configuration entries
// @Tags configuration
// @Security Bearer
// @Param request body ListConfigRequest true "Configuration scope"
// @Success 200 {object} Response{body=ConfigPageResponseBody}
// @Router /api/v1/config/entries/list [post]
func (h *Handler) ListConfig(c *gin.Context) {
	var request ListConfigRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		Fail(c, h.logger, apperror.Invalid("invalid json request", err))
		return
	}
	value, err := h.configs.List(c.Request.Context(), request.Environment, request.TenantID, request.ApplicationID, request.Service, request.Page, request.PageSize)
	if err != nil {
		Fail(c, h.logger, err)
		return
	}
	OK(c, ConfigPageResponseBody{Entries: configEntryResponses(value.Entries), Total: value.Total, Page: value.Page, PageSize: value.PageSize})
}

func configEntryResponses(entries []configdomain.Entry) []ConfigEntryResponseBody {
	result := make([]ConfigEntryResponseBody, 0, len(entries))
	for _, entry := range entries {
		result = append(result, configEntryResponse(entry))
	}
	return result
}

func configEntryResponse(entry configdomain.Entry) ConfigEntryResponseBody {
	var value any
	if len(entry.Value) > 0 {
		_ = json.Unmarshal(entry.Value, &value)
	}
	return ConfigEntryResponseBody{
		ID: entry.ID, Environment: entry.Environment, TenantID: entry.TenantID, ApplicationID: entry.ApplicationID, Service: entry.Service, Key: entry.Key,
		Value: value, SecretRef: entry.SecretRef, Status: entry.Status, Revision: entry.Revision,
		RolloutPercentage: entry.RolloutPercentage, PublishedRevision: entry.PublishedRevision,
		ReviewComment: entry.ReviewComment, ReviewedBy: entry.ReviewedBy, ReviewedAt: entry.ReviewedAt,
		Version: entry.Version, CreatedAt: entry.CreatedAt, UpdatedAt: entry.UpdatedAt,
		CreatedBy: entry.CreatedBy, UpdatedBy: entry.UpdatedBy,
	}
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
