//go:build integration

package integration

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lihongjie0209/config-service/internal/config"
	appdb "github.com/lihongjie0209/config-service/internal/database"
	configdomain "github.com/lihongjie0209/config-service/internal/dynamicconfig"
	"github.com/lihongjie0209/config-service/internal/migration"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/mysql"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestRepositoryAndMigrations(t *testing.T) {
	for _, databaseType := range []string{"postgres", "mysql"} {
		t.Run(databaseType, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
			defer cancel()
			dsn, migrationURL := startDatabase(t, ctx, databaseType)
			migrationPath, err := filepath.Abs(filepath.Join("..", "migrations", databaseType))
			if err != nil {
				t.Fatal(err)
			}
			schema := ""
			if databaseType == "postgres" {
				schema = "integration_postgres"
			}
			migrationCfg := config.Migration{Path: migrationPath, DatabaseURL: migrationURL, Table: "integration_" + databaseType + "_schema_migrations", Schema: schema, CreateSchema: schema != ""}
			migrationErrors := make(chan error, 3)
			var migrations sync.WaitGroup
			for range 3 {
				migrations.Add(1)
				go func() {
					defer migrations.Done()
					migrationErrors <- migration.Run(migrationCfg, "up", 0)
				}()
			}
			migrations.Wait()
			close(migrationErrors)
			for err := range migrationErrors {
				if err != nil {
					t.Fatalf("concurrent migration up: %v", err)
				}
			}

			db, err := appdb.Open(ctx, config.Database{Type: databaseType, DSN: dsn, Schema: schema, MaxOpenConns: 5, MaxIdleConns: 2, ConnMaxLifetime: time.Minute, ConnMaxIdleTime: time.Minute, PingTimeout: 10 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			service := configdomain.NewService(configdomain.NewRepository(db), appdb.NewTransactor(db))
			actorCtx := platformprincipal.WithContext(ctx, platformprincipal.Principal{ID: "admin-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1"})
			created, err := service.PutDraft(actorCtx, configdomain.Entry{Environment: "test", TenantID: "tenant-1", Service: "web", Key: "feature.checkout", Value: []byte(`true`), RolloutPercentage: 100}, 0)
			if err != nil {
				t.Fatalf("put draft: %v", err)
			}
			submitted, err := service.SubmitForApproval(actorCtx, created.ID, created.Version)
			if err != nil {
				t.Fatalf("submit: %v", err)
			}
			reviewerCtx := platformprincipal.WithContext(ctx, platformprincipal.Principal{ID: "reviewer-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1"})
			approved, err := service.Approve(reviewerCtx, created.ID, submitted.Version, "integration review")
			if err != nil {
				t.Fatalf("approve: %v", err)
			}
			published, err := service.Publish(actorCtx, created.ID, approved.Version)
			if err != nil {
				t.Fatalf("publish: %v", err)
			}
			resolved, etag, err := service.Resolve(actorCtx, "test", "tenant-1", "web", "user-1", []string{"feature.checkout"})
			if err != nil || len(resolved) != 1 || etag == "" || published.Status != "published" {
				t.Fatalf("resolve=%+v etag=%q published=%+v err=%v", resolved, etag, published, err)
			}
			var outboxEvents int
			if err := db.GetContext(ctx, &outboxEvents, `SELECT count(*) FROM config_outbox_events WHERE subject='platform.config.entry.changed.v1' AND published_at IS NULL`); err != nil || outboxEvents != 3 {
				t.Fatalf("pending config outbox events=%d err=%v", outboxEvents, err)
			}
			var userTables int
			if databaseType == "postgres" {
				if err := db.GetContext(ctx, &userTables, `SELECT count(*) FROM pg_tables WHERE schemaname = current_schema() AND tablename = 'users'`); err != nil {
					t.Fatal(err)
				}
				var timezone string
				if err := db.GetContext(ctx, &timezone, `SHOW TIMEZONE`); err != nil || timezone != "Asia/Shanghai" {
					t.Fatalf("timezone=%q err=%v", timezone, err)
				}
			} else if err := db.GetContext(ctx, &userTables, `SELECT count(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = 'users'`); err != nil {
				t.Fatal(err)
			}
			if userTables != 0 {
				t.Fatal("generic template migration must not create a users table")
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			if err := migration.Run(migrationCfg, "down", 0); err != nil {
				t.Fatalf("migration down: %v", err)
			}
		})
	}
}

func startDatabase(t *testing.T, ctx context.Context, databaseType string) (string, string) {
	t.Helper()
	switch databaseType {
	case "postgres":
		container, err := postgres.Run(ctx, "postgres:17-alpine", postgres.WithDatabase("app"), postgres.WithUsername("app"), postgres.WithPassword("app"), postgres.BasicWaitStrategies(), postgres.WithSQLDriver("pgx"))
		if err != nil {
			t.Fatal(err)
		}
		testcontainers.CleanupContainer(t, container)
		dsn, err := container.ConnectionString(ctx, "sslmode=disable")
		if err != nil {
			t.Fatal(err)
		}
		return dsn, dsn
	case "mysql":
		container, err := mysql.Run(ctx, "mysql:8.4", mysql.WithDatabase("app"), mysql.WithUsername("app"), mysql.WithPassword("app"))
		if err != nil {
			t.Fatal(err)
		}
		testcontainers.CleanupContainer(t, container)
		dsn, err := container.ConnectionString(ctx, "parseTime=true")
		if err != nil {
			t.Fatal(err)
		}
		migrationDSN, err := container.ConnectionString(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return dsn, "mysql://" + migrationDSN
	default:
		t.Fatal(fmt.Errorf("unsupported database %q", databaseType))
		return "", ""
	}
}
