package dynamicconfig

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/config-service/internal/apperror"
	"github.com/lihongjie0209/config-service/internal/database"
	"github.com/lihongjie0209/microservice-platform-go/appaccess"
	"github.com/lihongjie0209/microservice-platform-go/eventbus"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	configv1 "github.com/lihongjie0209/platform-protos/gen/go/platform/config/v1"
	"go.uber.org/fx"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Service struct {
	repository   Repository
	transactor   *database.Transactor
	now          func() time.Time
	applications appaccess.Verifier
}

type allowAllApplications struct{}

func (allowAllApplications) Verify(context.Context, string, string) error { return nil }

func NewService(repository Repository, transactor *database.Transactor) *Service {
	return &Service{repository: repository, transactor: transactor, applications: allowAllApplications{}, now: time.Now}
}

func NewRuntimeService(repository Repository, transactor *database.Transactor, applications appaccess.Verifier) (*Service, error) {
	if applications == nil {
		return nil, errors.New("application verifier is required")
	}
	return &Service{repository: repository, transactor: transactor, applications: applications, now: time.Now}, nil
}

func (s *Service) PutDraft(ctx context.Context, value Entry, expectedVersion int64) (Entry, error) {
	value.Environment = strings.ToLower(strings.TrimSpace(value.Environment))
	value.TenantID = strings.TrimSpace(value.TenantID)
	value.ApplicationID = strings.TrimSpace(value.ApplicationID)
	value.Service = strings.ToLower(strings.TrimSpace(value.Service))
	value.Key = strings.ToLower(strings.TrimSpace(value.Key))
	value.SecretRef = strings.TrimSpace(value.SecretRef)
	if value.Environment == "" || value.Service == "" || value.Key == "" || (value.TenantID == "" && value.ApplicationID != "") || value.RolloutPercentage < 0 || value.RolloutPercentage > 100 {
		return Entry{}, apperror.Invalid("invalid config scope or rollout percentage", nil)
	}
	if (len(value.Value) == 0) == (value.SecretRef == "") {
		return Entry{}, apperror.Invalid("exactly one of value or secret_ref is required", nil)
	}
	if sensitiveKey(value.Key) && value.SecretRef == "" {
		return Entry{}, apperror.Invalid("sensitive configuration must use secret_ref", nil)
	}
	caller, ok := platformprincipal.FromContext(ctx)
	if !ok {
		return Entry{}, apperror.Unauthorized("authenticated actor is required")
	}
	if err := enforceTenant(caller, value.TenantID); err != nil {
		return Entry{}, err
	}
	if err := s.verifyApplication(ctx, value.TenantID, value.ApplicationID); err != nil {
		return Entry{}, err
	}
	now := s.now()
	current, err := s.repository.GetByScope(ctx, value.Environment, value.TenantID, value.ApplicationID, value.Service, value.Key)
	if errors.Is(err, ErrNotFound) {
		value.ID = uuid.NewString()
		value.Status = "draft"
		value.Revision = 1
		value.Version = 1
		value.CreatedAt = now
		value.UpdatedAt = now
		value.CreatedBy = caller.ID
		value.UpdatedBy = caller.ID
		err = s.transactor.Within(ctx, nil, func(tx *sqlx.Tx) error { return s.repository.Insert(ctx, tx, value) })
		return value, translate(err)
	}
	if err != nil {
		return Entry{}, translate(err)
	}
	if expectedVersion < 1 {
		return Entry{}, apperror.Invalid("expected_version is required for an existing entry", nil)
	}
	current.Value, current.SecretRef, current.Status, current.RolloutPercentage, current.Revision, current.UpdatedAt, current.UpdatedBy = value.Value, value.SecretRef, "draft", value.RolloutPercentage, current.Revision+1, now, caller.ID
	current.ReviewComment, current.ReviewedBy, current.ReviewedAt = "", "", nil
	err = s.transactor.Within(ctx, nil, func(tx *sqlx.Tx) error { return s.repository.Update(ctx, tx, current, expectedVersion) })
	if err != nil {
		return Entry{}, translate(err)
	}
	current.Version = expectedVersion + 1
	return current, nil
}
func (s *Service) Publish(ctx context.Context, id string, expected int64) (Entry, error) {
	current, err := s.repository.Get(ctx, id)
	if err != nil {
		return Entry{}, translate(err)
	}
	if current.Status != "approved" {
		return Entry{}, apperror.Conflict("only an approved configuration can be published", nil)
	}
	current.PublishedRevision = current.Revision
	return s.changeStatus(ctx, current, expected, "published", false)
}
func (s *Service) SubmitForApproval(ctx context.Context, id string, expected int64) (Entry, error) {
	current, err := s.repository.Get(ctx, id)
	if err != nil {
		return Entry{}, translate(err)
	}
	if current.Status != "draft" && current.Status != "rejected" {
		return Entry{}, apperror.Conflict("only a draft or rejected configuration can be submitted", nil)
	}
	current.ReviewComment, current.ReviewedBy, current.ReviewedAt = "", "", nil
	return s.changeStatus(ctx, current, expected, "pending_approval", true)
}
func (s *Service) Approve(ctx context.Context, id string, expected int64, comment string) (Entry, error) {
	return s.review(ctx, id, expected, "approved", comment)
}
func (s *Service) Reject(ctx context.Context, id string, expected int64, reason string) (Entry, error) {
	if strings.TrimSpace(reason) == "" {
		return Entry{}, apperror.Invalid("rejection reason is required", nil)
	}
	return s.review(ctx, id, expected, "rejected", reason)
}
func (s *Service) review(ctx context.Context, id string, expected int64, statusValue, comment string) (Entry, error) {
	current, err := s.repository.Get(ctx, id)
	if err != nil {
		return Entry{}, translate(err)
	}
	if current.Status != "pending_approval" {
		return Entry{}, apperror.Conflict("configuration is not pending approval", nil)
	}
	caller, ok := platformprincipal.FromContext(ctx)
	if !ok {
		return Entry{}, apperror.Unauthorized("authenticated actor is required")
	}
	if err := enforceTenant(caller, current.TenantID); err != nil {
		return Entry{}, err
	}
	if err := s.verifyApplication(ctx, current.TenantID, current.ApplicationID); err != nil {
		return Entry{}, err
	}
	if caller.ID == current.UpdatedBy {
		return Entry{}, apperror.Conflict("submitter cannot review their own configuration", nil)
	}
	now := s.now()
	current.Status, current.ReviewComment, current.ReviewedBy, current.ReviewedAt = statusValue, strings.TrimSpace(comment), caller.ID, &now
	current.UpdatedAt, current.UpdatedBy = now, caller.ID
	err = s.transactor.Within(ctx, nil, func(tx *sqlx.Tx) error {
		if err := s.repository.UpdateRevisionReview(ctx, tx, current, "pending_approval"); err != nil {
			return err
		}
		if err := s.repository.Update(ctx, tx, current, expected); err != nil {
			return err
		}
		current.Version = expected + 1
		return s.addChangedEvent(ctx, tx, current, statusValue, caller.ID)
	})
	return current, translate(err)
}
func (s *Service) changeStatus(ctx context.Context, current Entry, expected int64, statusValue string, snapshot bool) (Entry, error) {
	caller, ok := platformprincipal.FromContext(ctx)
	if !ok {
		return Entry{}, apperror.Unauthorized("authenticated actor is required")
	}
	if err := enforceTenant(caller, current.TenantID); err != nil {
		return Entry{}, err
	}
	if err := s.verifyApplication(ctx, current.TenantID, current.ApplicationID); err != nil {
		return Entry{}, err
	}
	current.Status = statusValue
	current.UpdatedAt = s.now()
	current.UpdatedBy = caller.ID
	err := s.transactor.Within(ctx, nil, func(tx *sqlx.Tx) error {
		if err := s.repository.Update(ctx, tx, current, expected); err != nil {
			return err
		}
		current.Version = expected + 1
		if snapshot {
			if err := s.repository.Snapshot(ctx, tx, current); err != nil {
				return err
			}
		}
		return s.addChangedEvent(ctx, tx, current, statusValue, caller.ID)
	})
	return current, translate(err)
}
func (s *Service) Rollback(ctx context.Context, id string, target, expected int64) (Entry, error) {
	revision, err := s.repository.GetRevision(ctx, id, target)
	if err != nil {
		return Entry{}, translate(err)
	}
	current, err := s.repository.Get(ctx, id)
	if err != nil {
		return Entry{}, translate(err)
	}
	if revision.Status != "approved" && revision.Status != "published" {
		return Entry{}, apperror.Conflict("rollback target was not approved", nil)
	}
	current.Value, current.SecretRef, current.RolloutPercentage, current.Status, current.Revision = revision.Value, revision.SecretRef, revision.RolloutPercentage, "published", current.Revision+1
	current.PublishedRevision = current.Revision
	caller, ok := platformprincipal.FromContext(ctx)
	if !ok {
		return Entry{}, apperror.Unauthorized("authenticated actor is required")
	}
	if err := enforceTenant(caller, current.TenantID); err != nil {
		return Entry{}, err
	}
	if err := s.verifyApplication(ctx, current.TenantID, current.ApplicationID); err != nil {
		return Entry{}, err
	}
	current.UpdatedAt = s.now()
	current.UpdatedBy = caller.ID
	err = s.transactor.Within(ctx, nil, func(tx *sqlx.Tx) error {
		if err := s.repository.Update(ctx, tx, current, expected); err != nil {
			return err
		}
		current.Version = expected + 1
		if err := s.repository.Snapshot(ctx, tx, current); err != nil {
			return err
		}
		return s.addChangedEvent(ctx, tx, current, "rolled_back", caller.ID)
	})
	return current, translate(err)
}

func (s *Service) addChangedEvent(ctx context.Context, tx *sqlx.Tx, entry Entry, changeType, actor string) error {
	payload := &configv1.ConfigChangedEvent{Entry: &configv1.ConfigEntry{Id: entry.ID, Environment: entry.Environment, TenantId: entry.TenantID, ApplicationId: entry.ApplicationID, Service: entry.Service, Key: entry.Key, SecretRef: entry.SecretRef, Status: entry.Status, Revision: entry.Revision, RolloutPercentage: entry.RolloutPercentage, PublishedRevision: entry.PublishedRevision, ReviewComment: entry.ReviewComment, ReviewedBy: entry.ReviewedBy, Version: entry.Version, CreatedAt: timestamppb.New(entry.CreatedAt), UpdatedAt: timestamppb.New(entry.UpdatedAt), CreatedBy: entry.CreatedBy, UpdatedBy: entry.UpdatedBy}, ChangeType: changeType}
	if entry.ReviewedAt != nil {
		payload.Entry.ReviewedAt = timestamppb.New(*entry.ReviewedAt)
	}
	if entry.SecretRef == "" {
		payload.Entry.Value = append([]byte(nil), entry.Value...)
	}
	envelope, err := eventbus.NewEnvelope(eventbus.Metadata{EventID: uuid.NewString(), EventType: "platform.config.v1.ConfigChanged", AggregateID: entry.ID, AggregateType: "config_entry", TenantID: entry.TenantID, ApplicationID: entry.ApplicationID, SchemaVersion: 1, ActorID: actor, OccurredAt: entry.UpdatedAt}, payload)
	if err != nil {
		return fmt.Errorf("build config changed event: %w", err)
	}
	encoded, err := proto.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("encode config changed event: %w", err)
	}
	return s.repository.AddOutbox(ctx, tx, OutboxEvent{ID: envelope.GetEventId(), Subject: "platform.config.entry.changed.v1", Envelope: encoded, AvailableAt: entry.UpdatedAt, CreatedAt: entry.UpdatedAt, UpdatedAt: entry.UpdatedAt, CreatedBy: actor, UpdatedBy: actor})
}
func (s *Service) Resolve(ctx context.Context, environment, tenantID, applicationID, service, subject string, keys []string) ([]Entry, string, error) {
	caller, ok := platformprincipal.FromContext(ctx)
	if !ok {
		return nil, "", apperror.Unauthorized("authenticated actor is required")
	}
	if err := enforceTenant(caller, tenantID); err != nil {
		return nil, "", err
	}
	if err := s.verifyApplication(ctx, tenantID, applicationID); err != nil {
		return nil, "", err
	}
	values, err := s.repository.Resolve(ctx, environment, tenantID, applicationID, service, keys)
	if err != nil {
		return nil, "", translate(err)
	}
	selected := make(map[string]Entry)
	for _, value := range values {
		if _, exists := selected[value.Key]; exists {
			continue
		}
		if rolloutHit(subject, value.Key, value.Revision, value.RolloutPercentage) {
			selected[value.Key] = value
		}
	}
	result := make([]Entry, 0, len(selected))
	hash := sha256.New()
	for _, key := range keys {
		if value, ok := selected[key]; ok {
			result = append(result, value)
			_, _ = fmt.Fprintf(hash, "%s:%d;", value.ID, value.Revision)
		}
	}
	return result, fmt.Sprintf("%x", hash.Sum(nil)), nil
}
func (s *Service) List(ctx context.Context, environment, tenantID, applicationID, service string, page, pageSize int) (Page, error) {
	caller, ok := platformprincipal.FromContext(ctx)
	if !ok {
		return Page{}, apperror.Unauthorized("authenticated actor is required")
	}
	if err := enforceTenant(caller, tenantID); err != nil {
		return Page{}, err
	}
	if err := s.verifyApplication(ctx, tenantID, applicationID); err != nil {
		return Page{}, err
	}
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 20
	}
	if pageSize > 100 {
		return Page{}, apperror.Invalid("page_size must not exceed 100", nil)
	}
	values, total, err := s.repository.List(ctx, environment, tenantID, applicationID, service, pageSize, (page-1)*pageSize)
	return Page{Entries: values, Total: total, Page: page, PageSize: pageSize}, translate(err)
}
func enforceTenant(caller platformprincipal.Principal, requestedTenantID string) error {
	if caller.Type == platformprincipal.TypeUser && (strings.TrimSpace(caller.TenantID) == "" || caller.TenantID != strings.TrimSpace(requestedTenantID)) {
		return apperror.Forbidden("tenant access denied")
	}
	return nil
}

func (s *Service) verifyApplication(ctx context.Context, tenantID, applicationID string) error {
	tenantID, applicationID = strings.TrimSpace(tenantID), strings.TrimSpace(applicationID)
	if applicationID == "" {
		return nil
	}
	if tenantID == "" {
		return apperror.Invalid("application_id requires tenant_id", nil)
	}
	if err := s.applications.Verify(ctx, tenantID, applicationID); err != nil {
		if errors.Is(err, appaccess.ErrNotGranted) {
			return apperror.Forbidden("application access denied")
		}
		return apperror.Unavailable("application access verification unavailable", err)
	}
	return nil
}
func sensitiveKey(key string) bool {
	for _, part := range []string{"password", "secret", "token", "private_key", "api_key"} {
		if strings.Contains(key, part) {
			return true
		}
	}
	return false
}
func rolloutHit(subject, key string, revision int64, percentage int32) bool {
	if percentage >= 100 {
		return true
	}
	if percentage <= 0 || subject == "" {
		return false
	}
	sum := sha256.Sum256([]byte(subject + "\x00" + key + fmt.Sprint(revision)))
	bucket := int(sum[0])<<8 | int(sum[1])
	return bucket%100 < int(percentage)
}
func translate(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrNotFound) {
		return apperror.NotFound("config entry not found")
	}
	if errors.Is(err, ErrStaleVersion) {
		return apperror.StaleVersion(err)
	}
	return apperror.Internal(err)
}

var Module = fx.Module("dynamic-config", fx.Provide(NewRepository, NewRuntimeService))
