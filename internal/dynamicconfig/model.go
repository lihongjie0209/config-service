package dynamicconfig

import "time"

type Entry struct {
	ID                string     `db:"id" json:"id"`
	Environment       string     `db:"environment" json:"environment"`
	TenantID          string     `db:"tenant_id" json:"tenant_id"`
	Service           string     `db:"service" json:"service"`
	Key               string     `db:"config_key" json:"key"`
	Value             []byte     `db:"config_value" json:"value"`
	SecretRef         string     `db:"secret_ref" json:"secret_ref"`
	Status            string     `db:"status" json:"status"`
	Revision          int64      `db:"revision" json:"revision"`
	RolloutPercentage int32      `db:"rollout_percentage" json:"rollout_percentage"`
	PublishedRevision int64      `db:"published_revision" json:"published_revision"`
	ReviewComment     string     `db:"review_comment" json:"review_comment"`
	ReviewedBy        string     `db:"reviewed_by" json:"reviewed_by"`
	ReviewedAt        *time.Time `db:"reviewed_at" json:"reviewed_at,omitempty"`
	Version           int64      `db:"version" json:"version"`
	CreatedAt         time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt         time.Time  `db:"updated_at" json:"updated_at"`
	CreatedBy         string     `db:"created_by" json:"created_by"`
	UpdatedBy         string     `db:"updated_by" json:"updated_by"`
}
type Page struct {
	Entries  []Entry `json:"entries"`
	Total    int64   `json:"total"`
	Page     int     `json:"page"`
	PageSize int     `json:"page_size"`
}

type OutboxEvent struct {
	ID          string
	Subject     string
	Envelope    []byte
	AvailableAt time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
	CreatedBy   string
	UpdatedBy   string
}
