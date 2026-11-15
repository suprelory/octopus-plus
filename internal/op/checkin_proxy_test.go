package op

import (
	"context"
	"fmt"
	"testing"

	dbpkg "github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
)

func TestIndependentCheckinProxyReferencesProtectAllOwners(t *testing.T) {
	ctx := setupSiteOpTestDB(t)
	proxyConfigurationCache.Clear()
	t.Cleanup(proxyConfigurationCache.Clear)
	// Existing installations can use the proxy pool before v2 is prepared.
	unused := &model.ProxyConfiguration{Name: "unused", URL: "socks5h://unused.example:1080", Enabled: true}
	if err := ProxyConfigurationCreate(unused, ctx); err != nil {
		t.Fatal(err)
	}
	if counts, err := ProxyConfigurationReferenceCounts(ctx); err != nil || len(counts) != 0 {
		t.Fatalf("proxy counts required absent v2 tables: %v", err)
	}
	if refs, err := ProxyConfigurationReferences(unused.ID, ctx); err != nil || len(refs) != 0 {
		t.Fatalf("proxy references required absent v2 tables: %v", err)
	}
	if err := ProxyConfigurationDelete(unused.ID, ctx); err != nil {
		t.Fatal(err)
	}
	if dbpkg.GetDB().Migrator().HasTable(&model.CheckinSite{}) {
		t.Fatal("proxy read unexpectedly created checkin tables")
	}
	if err := dbpkg.PrepareCheckinSchema(dbpkg.GetDB()); err != nil {
		t.Fatal(err)
	}
	store := NewCheckinStore(dbpkg.GetDB())
	proxies := make([]model.ProxyConfiguration, 4)
	for i := range proxies {
		proxies[i] = model.ProxyConfiguration{Name: fmt.Sprintf("proxy-%d", i), URL: fmt.Sprintf("socks5h://proxy-%d.example:1080", i), Enabled: true}
		if err := ProxyConfigurationCreate(&proxies[i], ctx); err != nil {
			t.Fatal(err)
		}
	}
	network := func(i int) model.CheckinNetwork {
		return model.CheckinNetwork{ProxyMode: model.ProxyUsageModePool, ProxyConfigID: &proxies[i].ID}
	}
	site, err := store.CreateSite(ctx, model.CheckinSite{Name: "independent", Platform: model.SitePlatformSub2API, BaseURL: "https://checkin.example",
		CheckinHTTPEnabled: true, CheckinHTTPPath: "/checkin", CheckinNetwork: network(0)})
	if err != nil {
		t.Fatal(err)
	}
	candidate := testCheckinCredential()
	candidate.CheckinNetwork = network(2)
	credential, err := store.CreateCredential(ctx, candidate)
	if err != nil {
		t.Fatal(err)
	}
	account, err := store.CreateAccount(ctx, model.CheckinAccount{SiteID: site.ID, Name: "disabled", CredentialID: credential.ID,
		AuthMode: model.CheckinAuthBearer, CheckinNetwork: network(1),
		BalanceConfig: model.CheckinBalanceConfig{Mode: model.CheckinBalanceCustom, Platform: credential.Platform,
			BaseURL: credential.AuthBaseURL, CredentialID: &credential.ID, CheckinNetwork: network(3)}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ArchiveSite(ctx, site.ID); err != nil {
		t.Fatal(err)
	}
	wantTypes := []model.ProxyConfigurationReferenceType{model.ProxyConfigurationReferenceTypeCheckinSite,
		model.ProxyConfigurationReferenceTypeCheckinAccount, model.ProxyConfigurationReferenceTypeCheckinCredential, model.ProxyConfigurationReferenceTypeCheckinBalance}
	counts, err := ProxyConfigurationReferenceCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i, proxy := range proxies {
		if counts[proxy.ID] != 1 {
			t.Fatalf("proxy %d count = %d", i, counts[proxy.ID])
		}
		refs, err := ProxyConfigurationReferences(proxy.ID, ctx)
		if err != nil || len(refs) != 1 || refs[0].Type != wantTypes[i] {
			t.Fatalf("proxy %d reference missing: %+v %v", i, refs, err)
		}
		if i == 2 {
			if refs[0].CheckinCredentialID != credential.ID {
				t.Fatal("credential owner missing")
			}
		} else if refs[0].CheckinSiteID != site.ID || !refs[0].CheckinSiteArchived || (i != 0 && refs[0].CheckinAccountID != account.ID) {
			t.Fatal("archived site/account reference missing")
		}
		disabled := false
		if _, err := ProxyConfigurationUpdate(&model.ProxyConfigurationUpdateRequest{ID: proxy.ID, Enabled: &disabled}, ctx); err != nil {
			t.Fatal(err)
		}
		if err := ProxyConfigurationDelete(proxy.ID, ctx); err == nil {
			t.Fatalf("deleted disabled proxy %d despite archived/disabled owner", i)
		}
	}
	listed, err := ProxyConfigurationList(ctx)
	if err != nil || len(listed) != 4 {
		t.Fatalf("proxy list failed: %v", err)
	}
	for _, proxy := range listed {
		if proxy.ReferenceCount != 1 {
			t.Fatal("proxy list omitted v2 references")
		}
	}
	if err := store.DeleteAccount(ctx, account.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteSite(ctx, site.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteCredential(ctx, credential.ID); err != nil {
		t.Fatal(err)
	}
	for _, proxy := range proxies {
		if err := ProxyConfigurationDelete(proxy.ID, ctx); err != nil {
			t.Fatalf("unused proxy remains protected: %v", err)
		}
	}
}

func TestIndependentCheckinUnavailableProxyRollsBackInlineCredential(t *testing.T) {
	store, ctx := independentCheckinStore(t), context.Background()
	if err := store.conn.AutoMigrate(&model.ProxyConfiguration{}); err != nil {
		t.Fatal(err)
	}
	site := createIndependentSite(t, store)
	proxy := model.ProxyConfiguration{Name: "disabled", URL: "socks5h://proxy.example:1080", Enabled: true}
	if err := store.conn.Create(&proxy).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.conn.Model(&proxy).Update("enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	for _, proxyID := range []int{proxy.ID, proxy.ID + 1000} {
		credential := testCheckinCredential()
		credential.CheckinNetwork = model.CheckinNetwork{ProxyMode: model.ProxyUsageModePool, ProxyConfigID: &proxyID}
		if _, err := store.CreateAccount(ctx, model.CheckinAccount{SiteID: site.ID, Name: "inline", AuthMode: model.CheckinAuthBearer}, &credential); err == nil {
			t.Fatal("missing or disabled authentication proxy silently accepted")
		}
	}
	if credentials, err := store.ListCredentials(ctx); err != nil || len(credentials) != 0 {
		t.Fatalf("failed account creation left credential owner: %v", err)
	}
	if refs, err := store.ProxyReferences(ctx, proxy.ID); err != nil || len(refs) != 0 {
		t.Fatalf("proxy references require legacy tables or retained rollback: %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := store.ProxyReferences(cancelled, proxy.ID); err == nil {
		t.Fatal("cancelled metadata query silently reported no references")
	}
}
