package backup

import (
	"github.com/suprelory/octopus-plus/internal/model"
)

func (s *importer) network(network *model.CheckinNetwork) error {
	if network.ProxyMode != model.ProxyUsageModePool {
		if network.ProxyConfigID != nil {
			return ErrInvalid
		}
		return nil
	}
	if network.ProxyConfigID == nil {
		return ErrInvalid
	}
	id, ok := s.configuration.Proxies[*network.ProxyConfigID]
	if !ok {
		return ErrInvalid
	}
	network.ProxyConfigID = &id
	return nil
}

func (s *importer) nameAvailable(row any, name string, siteID int) error {
	query := s.tx.Model(row).Where("name = ?", name)
	if siteID != 0 {
		query = query.Where("site_id = ?", siteID)
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return err
	}
	if count != 0 {
		return ErrConflict
	}
	return nil
}

func (s *importer) importCheckin() error {
	d := s.document
	originalSites := map[int]model.CheckinSite{}
	originalCredentials := map[int]model.CheckinCredential{}
	for _, source := range d.CheckinSites {
		row := source.CheckinSite
		originalSites[row.ID] = row
		verified := source.VerificationFingerprint != "" && source.VerificationFingerprint == row.ConfigFingerprint()
		if err := row.Validate(); err != nil {
			return ErrInvalid
		}
		if err := s.network(&row.CheckinNetwork); err != nil {
			return err
		}
		if err := s.nameAvailable(&row, row.Name, 0); err != nil {
			return err
		}
		row.ID, row.VerificationFingerprint = 0, ""
		if verified {
			row.VerificationFingerprint = row.ConfigFingerprint()
		}
		if err := s.insert("checkin_sites", &row); err != nil {
			return err
		}
		s.sites[source.ID] = row
	}
	for _, source := range d.CheckinCredentials {
		row := source.Credential()
		originalCredentials[row.ID] = row
		if err := row.Validate(); err != nil {
			return ErrInvalid
		}
		if err := s.network(&row.CheckinNetwork); err != nil {
			return err
		}
		row.ID = 0
		if err := s.insert("checkin_credentials", &row); err != nil {
			return err
		}
		s.credentials[source.ID] = row
	}
	for _, source := range d.CheckinAccounts {
		row := source.CheckinAccount
		originalSite, siteExists := originalSites[row.SiteID]
		credential, credentialExists := s.credentials[row.CredentialID]
		if !siteExists || row.CredentialID != 0 && !credentialExists || source.WorkflowVersion < 0 || source.BalanceVersion < 0 || source.BalanceConfigVersion < 0 {
			return ErrInvalid
		}
		site := s.sites[row.SiteID]
		validWorkflow := row.CheckinPending != nil && credentialExists && row.CheckinPending.Fingerprint == originalSite.WorkflowFingerprint(row, originalCredentials[row.CredentialID])
		row.SiteID, row.CredentialID = site.ID, credential.ID
		row.CheckinWorkflowVersion, row.BalanceVersion, row.BalanceConfigVersion = source.WorkflowVersion, source.BalanceVersion, source.BalanceConfigVersion
		if row.BalanceConfig.CredentialID != nil {
			balanceCredential, ok := s.credentials[*row.BalanceConfig.CredentialID]
			if !ok {
				return ErrInvalid
			}
			if err := model.ValidateCheckinBalanceCredential(row.BalanceConfig, balanceCredential); err != nil {
				return ErrInvalid
			}
			row.BalanceConfig.CredentialID = &balanceCredential.ID
		}
		if err := s.network(&row.CheckinNetwork); err != nil {
			return err
		}
		if err := s.network(&row.BalanceConfig.CheckinNetwork); err != nil {
			return err
		}
		// P5.1 keeps accounts whose credentials were explicitly removed. Retain
		// that state while applying the ordinary validation to all other fields.
		if row.CredentialID == 0 {
			row.CredentialID = 1
		}
		if err := row.Validate(); err != nil {
			return ErrInvalid
		}
		row.CredentialID = credential.ID
		if credentialExists {
			if err := model.ValidateCheckinIdentity(site, row, credential); err != nil {
				return ErrInvalid
			}
			if row.BalanceConfig.Mode == model.CheckinBalancePrimary {
				if err := model.ValidateCheckinBalanceCredential(model.CheckinBalanceConfig{Platform: credential.Platform}, credential); err != nil {
					return ErrInvalid
				}
			}
		} else {
			if row.BalanceConfig.Mode == model.CheckinBalancePrimary {
				return ErrInvalid
			}
			row.AutoCheckin, row.NextAutoCheckinAt = false, nil
		}
		if pending := row.CheckinPending; pending != nil {
			if pending.LogID <= 0 || pending.BatchJobID < 0 {
				return ErrInvalid
			}
			pending.Fingerprint = "backup:invalid-workflow"
			if validWorkflow {
				pending.Fingerprint = site.WorkflowFingerprint(row, credential)
			}
		}
		if err := s.nameAvailable(&row, row.Name, row.SiteID); err != nil {
			return err
		}
		row.ID = 0
		if err := s.insert("checkin_accounts", &row); err != nil {
			return err
		}
		s.accounts[source.ID] = row
	}
	return nil
}
