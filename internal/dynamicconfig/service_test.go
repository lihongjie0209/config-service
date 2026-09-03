package dynamicconfig

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/config-service/internal/apperror"
	"github.com/lihongjie0209/config-service/internal/database"
	"github.com/lihongjie0209/microservice-platform-go/appaccess"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	commonv1 "github.com/lihongjie0209/platform-protos/gen/go/platform/common/v1"
	"google.golang.org/protobuf/proto"
)

type rejectingApplicationVerifier struct{ err error }

func (v rejectingApplicationVerifier) Verify(context.Context, string, string) error { return v.err }

type fakeRepository struct {
	entry    Entry
	resolved []Entry
	outbox   []OutboxEvent
}

func userContext(t *testing.T, id, tenantID string) context.Context {
	t.Helper()
	return platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: id, Type: platformprincipal.TypeUser, TenantID: tenantID})
}

func (f *fakeRepository) AddOutbox(_ context.Context, _ sqlx.ExtContext, event OutboxEvent) error {
	f.outbox = append(f.outbox, event)
	return nil
}

func (f *fakeRepository) Get(_ context.Context, id string) (Entry, error) {
	if f.entry.ID == id {
		return f.entry, nil
	}
	return Entry{}, ErrNotFound
}
func (f *fakeRepository) GetByScope(context.Context, string, string, string, string, string) (Entry, error) {
	if f.entry.ID == "" {
		return Entry{}, ErrNotFound
	}
	return f.entry, nil
}
func (f *fakeRepository) Insert(_ context.Context, _ sqlx.ExtContext, v Entry) error {
	f.entry = v
	return nil
}
func (f *fakeRepository) Update(_ context.Context, _ sqlx.ExtContext, v Entry, expected int64) error {
	if f.entry.Version != expected {
		return ErrStaleVersion
	}
	v.Version++
	f.entry = v
	return nil
}
func (*fakeRepository) Snapshot(context.Context, sqlx.ExtContext, Entry) error { return nil }
func (f *fakeRepository) UpdateRevisionReview(_ context.Context, _ sqlx.ExtContext, value Entry, expectedStatus string) error {
	if f.entry.Status != expectedStatus {
		return ErrStaleVersion
	}
	f.entry = value
	return nil
}
func (f *fakeRepository) GetRevision(context.Context, string, int64) (Entry, error) {
	return f.entry, nil
}
func (f *fakeRepository) Resolve(context.Context, string, string, string, string, []string) ([]Entry, error) {
	if f.resolved != nil {
		return f.resolved, nil
	}
	return []Entry{f.entry}, nil
}
func (f *fakeRepository) List(context.Context, string, string, string, string, int, int) ([]Entry, int64, error) {
	return []Entry{f.entry}, 1, nil
}

func TestPutDraftCreatesAuditedEntry(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectBegin()
	mock.ExpectCommit()
	repo := &fakeRepository{}
	service := NewService(repo, database.NewTransactor(sqlx.NewDb(db, "sqlmock")))
	now := time.Date(2026, 8, 30, 8, 0, 0, 0, time.FixedZone("UTC+8", 8*3600))
	service.now = func() time.Time { return now }
	ctx := userContext(t, "admin-1", "tenant-1")
	created, err := service.PutDraft(ctx, Entry{Environment: " Production ", TenantID: "tenant-1", Service: "Web", Key: "feature.checkout", Value: []byte(`true`), RolloutPercentage: 25}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if created.Status != "draft" || created.Version != 1 || created.CreatedBy != "admin-1" || created.Environment != "production" {
		t.Fatalf("created=%+v", created)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPutDraftRequiresSecretReferenceForSensitiveKey(t *testing.T) {
	service := NewService(&fakeRepository{}, &database.Transactor{})
	ctx := userContext(t, "admin-1", "tenant-1")
	_, err := service.PutDraft(ctx, Entry{Environment: "production", TenantID: "tenant-1", Service: "web", Key: "database.password", Value: []byte(`"plain"`), RolloutPercentage: 100}, 0)
	if err == nil {
		t.Fatal("PutDraft() accepted plaintext secret")
	}
}

func TestPublishApprovedRevisionCreatesTransactionalChangeEvent(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectBegin()
	mock.ExpectCommit()
	now := time.Date(2026, 8, 30, 8, 0, 0, 0, time.FixedZone("UTC+8", 8*3600))
	repository := &fakeRepository{entry: Entry{ID: "config-1", Environment: "production", TenantID: "tenant-1", ApplicationID: "app-1", Service: "orders", Key: "feature.checkout", Value: []byte("true"), Status: "approved", Revision: 1, RolloutPercentage: 100, Version: 1, CreatedAt: now, UpdatedAt: now, CreatedBy: "admin-1", UpdatedBy: "reviewer-1"}}
	service := NewService(repository, database.NewTransactor(sqlx.NewDb(db, "sqlmock")))
	service.now = func() time.Time { return now }
	ctx := userContext(t, "admin-1", "tenant-1")
	if _, err := service.Publish(ctx, "config-1", 1); err != nil {
		t.Fatal(err)
	}
	if len(repository.outbox) != 1 || repository.outbox[0].Subject != "platform.config.entry.changed.v1" || len(repository.outbox[0].Envelope) == 0 {
		t.Fatalf("outbox=%+v", repository.outbox)
	}
	envelope := &commonv1.EventEnvelope{}
	if err := proto.Unmarshal(repository.outbox[0].Envelope, envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.GetApplicationId() != "app-1" {
		t.Fatalf("event application_id = %q", envelope.GetApplicationId())
	}
	if repository.entry.PublishedRevision != 1 || repository.entry.Status != "published" {
		t.Fatalf("published=%+v", repository.entry)
	}
}

func TestPutDraftRejectsApplicationWithoutTenantGrant(t *testing.T) {
	service, err := NewRuntimeService(&fakeRepository{}, &database.Transactor{}, rejectingApplicationVerifier{err: appaccess.ErrNotGranted})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.PutDraft(userContext(t, "admin-1", "tenant-1"), Entry{Environment: "production", TenantID: "tenant-1", ApplicationID: "app-denied", Service: "web", Key: "feature.checkout", Value: []byte(`true`), RolloutPercentage: 100}, 0)
	var appErr *apperror.Error
	if !errors.As(err, &appErr) || appErr.Code != apperror.CodeForbidden {
		t.Fatalf("PutDraft() error = %v, want forbidden", err)
	}
}

func TestPublishRejectsUnapprovedRevision(t *testing.T) {
	repository := &fakeRepository{entry: Entry{ID: "config-1", Status: "draft", Version: 1}}
	service := NewService(repository, &database.Transactor{})
	ctx := userContext(t, "admin-1", "tenant-1")
	if _, err := service.Publish(ctx, "config-1", 1); err == nil {
		t.Fatal("Publish() accepted a draft revision")
	}
}

func TestApprovalRequiresIndependentReviewer(t *testing.T) {
	now := time.Date(2026, 8, 30, 8, 0, 0, 0, time.FixedZone("UTC+8", 8*3600))
	repository := &fakeRepository{entry: Entry{ID: "config-1", TenantID: "tenant-1", Status: "pending_approval", Revision: 1, Version: 2, UpdatedBy: "author-1", CreatedAt: now, UpdatedAt: now}}
	service := NewService(repository, &database.Transactor{})
	ctx := userContext(t, "author-1", "tenant-1")
	if _, err := service.Approve(ctx, "config-1", 2, "looks good"); err == nil {
		t.Fatal("Approve() allowed the submitter to approve their own revision")
	}
}

func TestIndependentReviewerCanApprove(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectBegin()
	mock.ExpectCommit()
	now := time.Date(2026, 8, 30, 8, 0, 0, 0, time.FixedZone("UTC+8", 8*3600))
	repository := &fakeRepository{entry: Entry{ID: "config-1", TenantID: "tenant-1", Status: "pending_approval", Revision: 1, Version: 2, UpdatedBy: "author-1", CreatedAt: now, UpdatedAt: now}}
	service := NewService(repository, database.NewTransactor(sqlx.NewDb(db, "sqlmock")))
	service.now = func() time.Time { return now }
	ctx := userContext(t, "reviewer-1", "tenant-1")
	approved, err := service.Approve(ctx, "config-1", 2, "reviewed")
	if err != nil {
		t.Fatal(err)
	}
	if approved.Status != "approved" || approved.ReviewedBy != "reviewer-1" || approved.Version != 3 || approved.ReviewedAt == nil {
		t.Fatalf("approved=%+v", approved)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRejectRequiresReason(t *testing.T) {
	service := NewService(&fakeRepository{entry: Entry{ID: "config-1", Status: "pending_approval"}}, &database.Transactor{})
	ctx := userContext(t, "reviewer-1", "tenant-1")
	if _, err := service.Reject(ctx, "config-1", 1, "  "); err == nil {
		t.Fatal("Reject() accepted an empty reason")
	}
}

func TestListRejectsTenantOutsideJWTContext(t *testing.T) {
	service := NewService(&fakeRepository{}, &database.Transactor{})
	_, err := service.List(userContext(t, "admin-1", "tenant-1"), "production", "tenant-2", "", "orders", 1, 20)
	var appErr *apperror.Error
	if !errors.As(err, &appErr) || appErr.Code != apperror.CodeForbidden {
		t.Fatalf("List() error = %v, want forbidden", err)
	}
}

func TestGetEnforcesPersistedTenantAndApplicationScope(t *testing.T) {
	t.Parallel()
	entry := Entry{ID: "config-1", TenantID: "tenant-1", ApplicationID: "app-1"}
	allowed := NewService(&fakeRepository{entry: entry}, &database.Transactor{})
	value, err := allowed.Get(userContext(t, "admin-1", "tenant-1"), " config-1 ")
	if err != nil || value.ID != entry.ID {
		t.Fatalf("Get() = %+v, %v", value, err)
	}
	if _, err := allowed.Get(userContext(t, "admin-1", "tenant-2"), "config-1"); err == nil {
		t.Fatal("Get() allowed a different tenant")
	}
	denied, err := NewRuntimeService(&fakeRepository{entry: entry}, &database.Transactor{}, rejectingApplicationVerifier{err: appaccess.ErrNotGranted})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := denied.Get(userContext(t, "admin-1", "tenant-1"), "config-1"); err == nil {
		t.Fatal("Get() allowed an ungranted application")
	}
}

func TestRolloutHitIsStable(t *testing.T) {
	first := rolloutHit("user-1", "feature.checkout", 3, 50)
	for range 10 {
		if rolloutHit("user-1", "feature.checkout", 3, 50) != first {
			t.Fatal("rollout assignment changed")
		}
	}
	if rolloutHit("user-1", "key", 1, 0) || !rolloutHit("user-1", "key", 1, 100) {
		t.Fatal("rollout boundary is wrong")
	}
}

func TestResolvePrefersApplicationThenTenantThenPlatform(t *testing.T) {
	repository := &fakeRepository{resolved: []Entry{
		{ID: "app", TenantID: "tenant-1", ApplicationID: "app-1", Key: "feature.checkout", Revision: 3, RolloutPercentage: 100},
		{ID: "tenant", TenantID: "tenant-1", Key: "feature.checkout", Revision: 2, RolloutPercentage: 100},
		{ID: "platform", Key: "feature.checkout", Revision: 1, RolloutPercentage: 100},
	}}
	service := NewService(repository, &database.Transactor{})
	values, _, err := service.Resolve(userContext(t, "user-1", "tenant-1"), "production", "tenant-1", "app-1", "orders", "user-1", []string{"feature.checkout"})
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 1 || values[0].ID != "app" {
		t.Fatalf("resolved = %+v", values)
	}
}
