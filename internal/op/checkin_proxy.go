package op

import (
	"context"
	"slices"

	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/gorm"
)

func (s *CheckinStore) ProxyReferences(ctx context.Context, id int) ([]model.ProxyConfigurationReference, error) {
	refs, err := checkinProxyReferences(s.conn.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	return append([]model.ProxyConfigurationReference{}, refs[id]...), nil
}

// Until cutover, existing installations may have none of these tables. This
// read-only bridge never creates tables and never loads credential secrets.
// Keep one collector for reports, counts and deletion protection, including
// disabled/archived owners and custom balance references stored inside JSON.
func checkinProxyReferences(conn *gorm.DB) (map[int][]model.ProxyConfigurationReference, error) {
	tables, err := conn.Migrator().GetTables()
	if err != nil {
		return nil, err
	}
	refs := map[int][]model.ProxyConfigurationReference{}
	add := func(network model.CheckinNetwork, ref model.ProxyConfigurationReference) {
		if network.ProxyMode == model.ProxyUsageModePool && network.ProxyConfigID != nil && *network.ProxyConfigID > 0 {
			refs[*network.ProxyConfigID] = append(refs[*network.ProxyConfigID], ref)
		}
	}
	var sites []model.CheckinSite
	if slices.Contains(tables, "checkin_sites") {
		if err := conn.Select("id", "name", "archived", "proxy_mode", "proxy_config_id").Order("id ASC").Find(&sites).Error; err != nil {
			return nil, err
		}
	}
	siteMap := make(map[int]model.CheckinSite, len(sites))
	for _, site := range sites {
		siteMap[site.ID] = site
		add(site.CheckinNetwork, model.ProxyConfigurationReference{
			Type:          model.ProxyConfigurationReferenceTypeCheckinSite,
			CheckinSiteID: site.ID, CheckinSiteName: site.Name, CheckinSiteArchived: site.Archived,
		})
	}
	var accounts []model.CheckinAccount
	if slices.Contains(tables, "checkin_accounts") {
		if err := conn.Select("id", "name", "site_id", "proxy_mode", "proxy_config_id", "balance_config").Order("id ASC").Find(&accounts).Error; err != nil {
			return nil, err
		}
	}
	for _, account := range accounts {
		site := siteMap[account.SiteID]
		ref := model.ProxyConfigurationReference{
			Type:          model.ProxyConfigurationReferenceTypeCheckinAccount,
			CheckinSiteID: account.SiteID, CheckinSiteName: site.Name, CheckinSiteArchived: site.Archived,
			CheckinAccountID: account.ID, CheckinAccountName: account.Name,
		}
		add(account.CheckinNetwork, ref)
		if account.BalanceConfig.Mode == model.CheckinBalanceCustom {
			ref.Type = model.ProxyConfigurationReferenceTypeCheckinBalance
			add(account.BalanceConfig.CheckinNetwork, ref)
		}
	}
	var credentials []model.CheckinCredential
	if slices.Contains(tables, "checkin_credentials") {
		if err := conn.Select("id", "name", "proxy_mode", "proxy_config_id").Order("id ASC").Find(&credentials).Error; err != nil {
			return nil, err
		}
	}
	for _, credential := range credentials {
		add(credential.CheckinNetwork, model.ProxyConfigurationReference{
			Type:                model.ProxyConfigurationReferenceTypeCheckinCredential,
			CheckinCredentialID: credential.ID, CheckinCredentialName: credential.Name,
		})
	}
	return refs, nil
}
