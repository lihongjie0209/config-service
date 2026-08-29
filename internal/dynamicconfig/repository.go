package dynamicconfig

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jmoiron/sqlx"
)

var ErrNotFound = errors.New("config entry not found")
var ErrStaleVersion = errors.New("stale config version")

type Repository interface {
	Get(context.Context, string) (Entry, error)
	GetByScope(context.Context, string, string, string, string) (Entry, error)
	Insert(context.Context, sqlx.ExtContext, Entry) error
	Update(context.Context, sqlx.ExtContext, Entry, int64) error
	Snapshot(context.Context, sqlx.ExtContext, Entry) error
	UpdateRevisionReview(context.Context, sqlx.ExtContext, Entry, string) error
	GetRevision(context.Context, string, int64) (Entry, error)
	Resolve(context.Context, string, string, string, []string) ([]Entry, error)
	List(context.Context, string, string, string, int, int) ([]Entry, int64, error)
	AddOutbox(context.Context, sqlx.ExtContext, OutboxEvent) error
}

func (r *SQLRepository) AddOutbox(ctx context.Context, exec sqlx.ExtContext, event OutboxEvent) error {
	_, err := exec.ExecContext(ctx, r.db.Rebind(`INSERT INTO config_outbox_events (id,subject,envelope,attempts,available_at,published_at,last_error,version,created_at,updated_at,created_by,updated_by) VALUES (?,?,?,0,?,NULL,'',1,?,?,?,?)`), event.ID, event.Subject, event.Envelope, event.AvailableAt, event.CreatedAt, event.UpdatedAt, event.CreatedBy, event.UpdatedBy)
	return err
}

type SQLRepository struct{ db *sqlx.DB }

func NewRepository(db *sqlx.DB) Repository { return &SQLRepository{db: db} }

const columns = `id, environment, tenant_id, service, config_key, config_value, secret_ref, status, revision, rollout_percentage, published_revision, review_comment, reviewed_by, reviewed_at, version, created_at, updated_at, created_by, updated_by`

func (r *SQLRepository) Get(ctx context.Context, id string) (Entry, error) {
	var v Entry
	err := r.db.GetContext(ctx, &v, r.db.Rebind(`SELECT `+columns+` FROM config_entries WHERE id=?`), id)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return v, err
}
func (r *SQLRepository) GetByScope(ctx context.Context, environment, tenantID, service, key string) (Entry, error) {
	var v Entry
	err := r.db.GetContext(ctx, &v, r.db.Rebind(`SELECT `+columns+` FROM config_entries WHERE environment=? AND tenant_id=? AND service=? AND config_key=?`), environment, tenantID, service, key)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return v, err
}
func (r *SQLRepository) Insert(ctx context.Context, e sqlx.ExtContext, v Entry) error {
	_, err := e.ExecContext(ctx, r.db.Rebind(`INSERT INTO config_entries (`+columns+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`), v.ID, v.Environment, v.TenantID, v.Service, v.Key, v.Value, v.SecretRef, v.Status, v.Revision, v.RolloutPercentage, v.PublishedRevision, v.ReviewComment, v.ReviewedBy, v.ReviewedAt, v.Version, v.CreatedAt, v.UpdatedAt, v.CreatedBy, v.UpdatedBy)
	return err
}
func (r *SQLRepository) Update(ctx context.Context, e sqlx.ExtContext, v Entry, expected int64) error {
	result, err := e.ExecContext(ctx, r.db.Rebind(`UPDATE config_entries SET config_value=?,secret_ref=?,status=?,revision=?,rollout_percentage=?,published_revision=?,review_comment=?,reviewed_by=?,reviewed_at=?,version=version+1,updated_at=?,updated_by=? WHERE id=? AND version=?`), v.Value, v.SecretRef, v.Status, v.Revision, v.RolloutPercentage, v.PublishedRevision, v.ReviewComment, v.ReviewedBy, v.ReviewedAt, v.UpdatedAt, v.UpdatedBy, v.ID, expected)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n == 0 {
		return ErrStaleVersion
	}
	return err
}
func (r *SQLRepository) Snapshot(ctx context.Context, e sqlx.ExtContext, v Entry) error {
	_, err := e.ExecContext(ctx, r.db.Rebind(`INSERT INTO config_revisions (id,entry_id,revision,config_value,secret_ref,status,rollout_percentage,review_comment,reviewed_by,reviewed_at,version,created_at,updated_at,created_by,updated_by) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`), v.ID+"-"+fmt.Sprint(v.Revision), v.ID, v.Revision, v.Value, v.SecretRef, v.Status, v.RolloutPercentage, v.ReviewComment, v.ReviewedBy, v.ReviewedAt, 1, v.UpdatedAt, v.UpdatedAt, v.UpdatedBy, v.UpdatedBy)
	return err
}
func (r *SQLRepository) UpdateRevisionReview(ctx context.Context, e sqlx.ExtContext, v Entry, expectedStatus string) error {
	result, err := e.ExecContext(ctx, r.db.Rebind(`UPDATE config_revisions SET status=?,review_comment=?,reviewed_by=?,reviewed_at=?,version=version+1,updated_at=?,updated_by=? WHERE entry_id=? AND revision=? AND status=?`), v.Status, v.ReviewComment, v.ReviewedBy, v.ReviewedAt, v.UpdatedAt, v.UpdatedBy, v.ID, v.Revision, expectedStatus)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n == 0 {
		return ErrStaleVersion
	}
	return err
}
func (r *SQLRepository) GetRevision(ctx context.Context, id string, revision int64) (Entry, error) {
	var v Entry
	err := r.db.GetContext(ctx, &v, r.db.Rebind(`SELECT e.id,e.environment,e.tenant_id,e.service,e.config_key,r.config_value,r.secret_ref,r.status,r.revision,r.rollout_percentage,e.published_revision,r.review_comment,r.reviewed_by,r.reviewed_at,r.version,r.created_at,r.updated_at,r.created_by,r.updated_by FROM config_revisions r JOIN config_entries e ON e.id=r.entry_id WHERE r.entry_id=? AND r.revision=?`), id, revision)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return v, err
}
func (r *SQLRepository) Resolve(ctx context.Context, environment, tenantID, service string, keys []string) ([]Entry, error) {
	if len(keys) == 0 {
		return []Entry{}, nil
	}
	query, args, err := sqlx.In(`SELECT e.id,e.environment,e.tenant_id,e.service,e.config_key,r.config_value,r.secret_ref,'published' AS status,r.revision,r.rollout_percentage,e.published_revision,r.review_comment,r.reviewed_by,r.reviewed_at,e.version,e.created_at,e.updated_at,e.created_by,e.updated_by FROM config_entries e JOIN config_revisions r ON r.entry_id=e.id AND r.revision=e.published_revision WHERE e.environment=? AND e.tenant_id IN (?, '') AND e.service=? AND e.published_revision>0 AND e.config_key IN (?) ORDER BY CASE WHEN e.tenant_id=? THEN 0 ELSE 1 END`, environment, tenantID, service, keys, tenantID)
	if err != nil {
		return nil, err
	}
	var values []Entry
	err = r.db.SelectContext(ctx, &values, r.db.Rebind(query), args...)
	return values, err
}
func (r *SQLRepository) List(ctx context.Context, environment, tenantID, service string, limit, offset int) ([]Entry, int64, error) {
	args := []any{environment, tenantID, service}
	where := ` WHERE environment=? AND tenant_id=? AND service=?`
	var total int64
	if err := r.db.GetContext(ctx, &total, r.db.Rebind(`SELECT count(*) FROM config_entries`+where), args...); err != nil {
		return nil, 0, err
	}
	args = append(args, limit, offset)
	var values []Entry
	err := r.db.SelectContext(ctx, &values, r.db.Rebind(`SELECT `+columns+` FROM config_entries`+where+` ORDER BY config_key LIMIT ? OFFSET ?`), args...)
	return values, total, err
}
