package op

import (
	"errors"
	"testing"

	mysqlerr "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// Driver error translation does not open a connection. Real constraint writes
// run through SQLite API tests and the external database integration suite.
func TestCheckinNameErrorDialects(t *testing.T) {
	for _, test := range []struct {
		name     string
		dialect  gorm.Dialector
		err      error
		conflict bool
	}{
		{"mysql duplicate", mysql.New(mysql.Config{}), &mysqlerr.MySQLError{Number: 1062, Message: "duplicate name"}, true},
		{"postgres duplicate", postgres.New(postgres.Config{}), &pgconn.PgError{Code: "23505", Message: "duplicate name"}, true},
		{"mysql storage error", mysql.New(mysql.Config{}), &mysqlerr.MySQLError{Number: 1146, Message: "table missing"}, false},
		{"postgres storage error", postgres.New(postgres.Config{}), &pgconn.PgError{Code: "42P01", Message: "table missing"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &CheckinStore{conn: &gorm.DB{Config: &gorm.Config{Dialector: test.dialect}}}
			err := store.checkinNameError(test.err)
			if errors.Is(err, ErrCheckinNameConflict) != test.conflict || !test.conflict && err != test.err {
				t.Fatalf("unexpected classification: %v", err)
			}
		})
	}
}

func testCheckinNameConstraints(t *testing.T, store *CheckinStore) {
	t.Helper()
	ctx := t.Context()
	site, err := store.CreateSite(ctx, testNameConflictSite("constraint-one"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateSite(ctx, *site); !errors.Is(err, ErrCheckinNameConflict) {
		t.Fatalf("duplicate site: %v", err)
	}
	other, err := store.CreateSite(ctx, testNameConflictSite("constraint-two"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PatchSite(ctx, other.ID, checkinPatch[model.CheckinSitePatch](t, `{"name":"constraint-one"}`)); !errors.Is(err, ErrCheckinNameConflict) {
		t.Fatalf("duplicate site patch: %v", err)
	}
	credential := testCheckinCredential()
	account, err := store.CreateAccount(ctx, model.CheckinAccount{SiteID: site.ID, Name: "one", AuthMode: model.CheckinAuthBearer}, &credential)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateAccount(ctx, *account, nil); !errors.Is(err, ErrCheckinNameConflict) {
		t.Fatalf("duplicate account: %v", err)
	}
	otherAccount := *account
	otherAccount.Name = "two"
	created, err := store.CreateAccount(ctx, otherAccount, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PatchAccount(ctx, created.ID, checkinPatch[model.CheckinAccountPatch](t, `{"name":"one"}`)); !errors.Is(err, ErrCheckinNameConflict) {
		t.Fatalf("duplicate account patch: %v", err)
	}
}

func testNameConflictSite(name string) model.CheckinSite {
	return model.CheckinSite{Name: name, Platform: model.SitePlatformAPI, BaseURL: "https://checkin.example", CheckinHTTPEnabled: true, CheckinHTTPPath: "/checkin"}
}

func TestIndependentCheckinNameConstraints(t *testing.T) {
	testCheckinNameConstraints(t, independentCheckinStore(t))
}
