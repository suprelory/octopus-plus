package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"

	v2 "github.com/suprelory/octopus-plus/internal/migration/v2"
	"github.com/suprelory/octopus-plus/internal/model"
)

func fingerprint(ctx context.Context, d *Document) (string, error) {
	copy := *d
	copy.RelayLogs, copy.RelayRequestContents, copy.RelayAttemptContents = nil, nil, nil
	hash := sha256.New()
	encoder := json.NewEncoder(hash)
	if err := encoder.Encode(copy); err != nil {
		return "", ErrFormat
	}
	if err := walkRelay(ctx, d, func(name string, row any) error {
		if err := encoder.Encode(name); err != nil {
			return err
		}
		return encoder.Encode(row)
	}); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func discardRetiredSettings(settings []model.Setting) []model.Setting {
	return slices.DeleteFunc(settings, func(setting model.Setting) bool { return setting.Key.Retired() })
}

// ConvertLegacy shares P5.1's conversion semantics, while keeping ordinary
// configuration, relay history and statistical archives in the backup document.
func ConvertLegacy(source *Document) (*Document, []model.BackupIssue, error) {
	if source == nil || source.Version != 1 {
		return nil, nil, ErrFormat
	}
	if len(source.CheckinSites)+len(source.CheckinCredentials)+len(source.CheckinAccounts)+len(source.CheckinCompletions)+len(source.CheckinDeliveries)+len(source.CheckinCooldowns) != 0 {
		return nil, nil, ErrFormat
	}
	input, err := v2.InputFromDump(&source.LegacyDBDump)
	if err != nil {
		return nil, nil, ErrFormat
	}
	converted, err := v2.Convert(input)
	if err != nil {
		return nil, nil, ErrInvalid
	}
	issues := make([]model.BackupIssue, 0, len(converted.Report.Issues))
	for _, issue := range converted.Report.Issues {
		issues = append(issues, model.BackupIssue{Code: issue.Code, Severity: issue.Severity, Entity: issue.Entity, ID: issue.ID})
	}
	if !converted.Ready() {
		return nil, issues, ErrInvalid
	}
	d := *source
	d.Version = Version
	d.Channels, d.ChannelKeys = converted.Channels, converted.ChannelKeys
	d.Groups, d.GroupItems, d.GroupPresets = converted.Groups, converted.GroupItems, converted.GroupPresets
	d.StatsChannel = converted.ChannelStats
	d.SiteCheckinLogs, d.SiteCheckinBatchJobs = converted.Logs, converted.Batches
	d.Settings = discardRetiredSettings(slices.Clone(d.Settings))
	for _, site := range converted.Sites {
		d.CheckinSites = append(d.CheckinSites, Site{site, site.VerificationFingerprint})
	}
	for _, credential := range converted.Credentials {
		d.CheckinCredentials = append(d.CheckinCredentials, model.ExportCheckinCredential(credential))
	}
	for _, account := range converted.Accounts {
		d.CheckinAccounts = append(d.CheckinAccounts, Account{account, account.CheckinWorkflowVersion, account.BalanceVersion, account.BalanceConfigVersion})
	}
	d.Sites, d.SiteAccounts, d.SiteTokens, d.SiteUserGroups, d.SiteModels, d.SiteChannelBindings = nil, nil, nil, nil, nil, nil
	return &d, issues, nil
}
