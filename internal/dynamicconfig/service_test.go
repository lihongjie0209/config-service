package dynamicconfig

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/config-service/internal/database"
	"github.com/lihongjie0209/config-service/internal/principal"
)

type fakeRepository struct {
	entry  Entry
	outbox []OutboxEvent
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
func (f *fakeRepository) GetByScope(context.Context, string, string, string, string) (Entry, error) {
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
func (f *fakeRepository) GetRevision(context.Context, string, int64) (Entry, error) {
	return f.entry, nil
}
func (f *fakeRepository) Resolve(context.Context, string, string, string, []string) ([]Entry, error) {
	return []Entry{f.entry}, nil
}
func (f *fakeRepository) List(context.Context, string, string, string, int, int) ([]Entry, int64, error) {
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
	ctx := principal.WithContext(t.Context(), principal.Principal{Subject: "admin-1", Method: principal.AuthenticationJWT})
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
	ctx := principal.WithContext(t.Context(), principal.Principal{Subject: "admin-1"})
	_, err := service.PutDraft(ctx, Entry{Environment: "production", Service: "web", Key: "database.password", Value: []byte(`"plain"`), RolloutPercentage: 100}, 0)
	if err == nil {
		t.Fatal("PutDraft() accepted plaintext secret")
	}
}

func TestPublishCreatesTransactionalChangeEvent(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectBegin()
	mock.ExpectCommit()
	now := time.Date(2026, 8, 30, 8, 0, 0, 0, time.FixedZone("UTC+8", 8*3600))
	repository := &fakeRepository{entry: Entry{ID: "config-1", Environment: "production", Service: "orders", Key: "feature.checkout", Value: []byte("true"), Status: "draft", Revision: 1, RolloutPercentage: 100, Version: 1, CreatedAt: now, UpdatedAt: now, CreatedBy: "admin-1", UpdatedBy: "admin-1"}}
	service := NewService(repository, database.NewTransactor(sqlx.NewDb(db, "sqlmock")))
	service.now = func() time.Time { return now }
	ctx := principal.WithContext(t.Context(), principal.Principal{Subject: "admin-1"})
	if _, err := service.Publish(ctx, "config-1", 1); err != nil {
		t.Fatal(err)
	}
	if len(repository.outbox) != 1 || repository.outbox[0].Subject != "platform.config.entry.changed.v1" || len(repository.outbox[0].Envelope) == 0 {
		t.Fatalf("outbox=%+v", repository.outbox)
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
