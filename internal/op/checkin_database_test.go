package op

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	mysqlconfig "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	dbpkg "github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Integration DSNs identify a disposable test server with CREATE DATABASE
// permission. Each subtest creates and drops only its own random database;
// existing databases/tables are never migrated, truncated or removed.
func independentCheckinExternalStore(t *testing.T, dialect, dsn string) (*CheckinStore, *gorm.DB) {
	t.Helper()
	open := func(database string) (*gorm.DB, error) {
		config := &gorm.Config{Logger: logger.Discard}
		if dialect == "mysql" {
			parsed, err := mysqlconfig.ParseDSN(dsn)
			if err != nil {
				return nil, errors.New("invalid MySQL integration DSN")
			}
			if database != "" {
				parsed.DBName = database
			}
			parsed.ParseTime = true
			return gorm.Open(mysql.Open(parsed.FormatDSN()), config)
		}
		parsed, err := pgx.ParseConfig(dsn)
		if err != nil {
			return nil, errors.New("invalid PostgreSQL integration DSN")
		}
		if database != "" {
			parsed.Database = database
		}
		pool := stdlib.OpenDB(*parsed)
		conn, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), config)
		if err != nil {
			_ = pool.Close()
		}
		return conn, err
	}
	admin, err := open("")
	if err != nil {
		t.Fatalf("cannot connect to %s integration server", dialect)
	}
	adminPool, err := admin.DB()
	if err != nil {
		t.Fatal("cannot access integration connection")
	}
	t.Cleanup(func() { _ = adminPool.Close() })
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	name := "checkin_it_" + hex.EncodeToString(random[:])
	quoted := "`" + name + "`"
	if dialect == "postgres" {
		quoted = `"` + name + `"`
	}
	if err := admin.Exec("CREATE DATABASE " + quoted).Error; err != nil {
		t.Fatalf("cannot create isolated %s integration database", dialect)
	}
	t.Cleanup(func() {
		if err := admin.Exec("DROP DATABASE " + quoted).Error; err != nil {
			t.Errorf("cannot remove generated integration database %s", name)
		}
	})
	conn, err := open(name)
	if err != nil {
		t.Fatalf("cannot open isolated %s integration database", dialect)
	}
	pool, err := conn.DB()
	if err != nil {
		t.Fatal(err)
	}
	pool.SetMaxOpenConns(16)
	t.Cleanup(func() { _ = pool.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := dbpkg.PrepareCheckinSchema(conn.WithContext(ctx)); err != nil {
		t.Fatal(err)
	}
	return NewCheckinStore(conn), conn
}

func TestIndependentCheckinDatabases(t *testing.T) {
	for _, database := range []struct{ dialect, env string }{{"mysql", "CHECKIN_TEST_MYSQL_DSN"}, {"postgres", "CHECKIN_TEST_POSTGRES_DSN"}} {
		t.Run(database.dialect, func(t *testing.T) {
			dsn := os.Getenv(database.env)
			if dsn == "" {
				t.Skip("set " + database.env + " to run database integration tests")
			}
			store, conn := independentCheckinExternalStore(t, database.dialect, dsn)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			credential, err := store.CreateCredential(ctx, testCheckinCredential())
			if err != nil {
				t.Fatal(err)
			}
			var wg sync.WaitGroup
			claims := make(chan *CheckinSessionClaim, 12)
			for range 12 {
				wg.Go(func() {
					claim, err := store.ClaimRefresh(ctx, credential.ID, 0, time.Minute)
					if err != nil && !errors.Is(err, ErrCheckinRefreshLease) {
						t.Errorf("concurrent lease failure: %v", err)
					}
					claims <- claim
				})
			}
			wg.Wait()
			close(claims)
			var owner *CheckinSessionClaim
			owners := 0
			for claim := range claims {
				if claim != nil {
					owner, owners = claim, owners+1
				}
			}
			if owners != 1 {
				t.Fatalf("refresh owner count=%d", owners)
			}
			if err := store.BeginSessionExchange(ctx, *owner); err != nil {
				t.Fatal(err)
			}
			if err := conn.Model(credential).Update("token_refresh_lease_until", 0).Error; err != nil {
				t.Fatal(err)
			}
			if _, err := store.ClaimRefresh(ctx, credential.ID, 0, time.Minute); !errors.Is(err, ErrCheckinSessionUncertain) {
				t.Fatalf("expired dispatched lease was stolen: %v", err)
			}
			candidate := *credential
			candidate.Name = "renamed"
			if _, err := store.EditCredential(ctx, candidate, 0); err != nil {
				t.Fatal(err)
			}
			pair := CheckinTokenPair{AccessToken: "new", RefreshToken: "rotated", TokenExpiresAt: time.Now().Add(time.Hour).UnixMilli()}
			for range 2 { // Include lost acknowledgement recovery.
				saved, err := store.CommitRefresh(ctx, *owner, pair)
				if err != nil || saved.CredentialVersion != 2 || saved.Name != "renamed" || saved.TokenRefreshStarted {
					t.Fatalf("atomic save did not preserve settings: %v", err)
				}
			}
			if _, err := store.EditCredential(ctx, candidate, 0); !errors.Is(err, ErrCheckinCredentialConflict) {
				t.Fatalf("stale editor overwrote rotated tokens: %v", err)
			}
			site := createIndependentSite(t, store)
			for attempt := range 12 {
				credential, err := store.CreateCredential(ctx, testCheckinCredential())
				if err != nil {
					t.Fatal(err)
				}
				var account *model.CheckinAccount
				var createErr, deleteErr error
				start := make(chan struct{})
				wg.Go(func() {
					<-start
					account, createErr = store.CreateAccount(ctx, model.CheckinAccount{SiteID: site.ID, Name: fmt.Sprintf("race-%d", attempt),
						AuthMode: model.CheckinAuthBearer, CredentialID: credential.ID}, nil)
				})
				wg.Go(func() { <-start; deleteErr = store.DeleteCredential(ctx, credential.ID) })
				close(start)
				wg.Wait()
				if createErr == nil {
					if account == nil || !errors.Is(deleteErr, ErrCheckinCredentialInUse) {
						t.Fatalf("created account lost its credential: %v", deleteErr)
					}
				} else if deleteErr != nil || !errors.Is(createErr, gorm.ErrRecordNotFound) {
					t.Fatalf("unexpected reference/delete conflict: create=%v delete=%v", createErr, deleteErr)
				}
			}
			site, err = store.PatchSite(ctx, site.ID, checkinPatch[model.CheckinSitePatch](t, `{"enabled":true}`))
			if err != nil {
				t.Fatal(err)
			}
			workflowCredential := testCheckinCredential()
			workflowAccount, err := store.CreateAccount(ctx, model.CheckinAccount{SiteID: site.ID, Name: "workflow", Enabled: true, AuthMode: model.CheckinAuthBearer}, &workflowCredential)
			if err != nil {
				t.Fatal(err)
			}
			testWorkflowReservationCAS(t, store, workflowAccount.ID)
			testPlatformSessionCAS(t, store)
			testScheduledReservationRace(t, store, site.ID, workflowAccount.ID)
			testBatchOwnership(t, store, site.ID, workflowAccount.ID)
			testBalanceNotificationCAS(t, store, workflowAccount.ID)
		})
	}
}
