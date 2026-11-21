package v2

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
)

// LegacyInput is a frozen migration input, not the configuration-only preflight
// snapshot. A database reader must explicitly load history and mark it complete.
// Backup readers use IncludeLogs to distinguish omitted history from an empty one.
// CapturedAt must stay fixed across retries of the same input.
type LegacyInput struct {
	Configuration     Snapshot
	CapturedAt        time.Time
	HistoryComplete   bool
	Logs              []model.CheckinLog
	Batches           []model.CheckinBatchJob
	GroupPresets      []model.GroupPreset
	ChannelStats      []model.StatsChannel
	ArchivedSiteStats []model.StatsSiteModelHourly
}

// InputFromDump adapts an already decoded v1 dump. ReadBackup intentionally
// remains configuration-only; it cannot be used as a full migration reader.
func InputFromDump(dump *model.DBDump) (*LegacyInput, error) {
	if dump == nil || dump.Version != 1 {
		return nil, errors.New("conversion requires a version 1 dump")
	}
	return &LegacyInput{
		Configuration: Snapshot{Version: dump.Version, ExportedAt: dump.ExportedAt,
			IncludeLogs: dump.IncludeLogs, IncludeStats: dump.IncludeStats,
			Sites: dump.Sites, SiteAccounts: dump.SiteAccounts, Channels: dump.Channels,
			ChannelKeys: dump.ChannelKeys, ProxyConfigurations: dump.ProxyConfigurations,
			SiteUserGroups: dump.SiteUserGroups, SiteChannelBindings: dump.SiteChannelBindings,
			Groups: dump.Groups, GroupItems: dump.GroupItems, Settings: dump.Settings},
		CapturedAt: dump.ExportedAt, HistoryComplete: dump.IncludeLogs,
		Logs: dump.SiteCheckinLogs, Batches: dump.SiteCheckinBatchJobs,
		GroupPresets: dump.GroupPresets, ChannelStats: dump.StatsChannel,
		ArchivedSiteStats: dump.StatsSiteModelHourly,
	}, nil
}

type Entity string

const (
	EntitySite       Entity = "site"
	EntityAccount    Entity = "account"
	EntityCredential Entity = "credential"
	EntityChannel    Entity = "channel"
)

// Mapping keys refer to legacy IDs, never to names or secret values. Multiple
// source IDs can resolve to one destination when earlier splitting left provenance.
type Mapping struct {
	Entity   Entity `json:"entity"`
	SourceID int    `json:"source_id"`
	TargetID int    `json:"target_id"`
}

type ConversionReport struct {
	Sites              int       `json:"sites"`
	Accounts           int       `json:"accounts"`
	Credentials        int       `json:"credentials"`
	ConvertedChannels  int       `json:"converted_channels"`
	Logs               int       `json:"logs"`
	RebuiltLogs        int       `json:"rebuilt_logs"`
	InterruptedBatches int       `json:"interrupted_batches"`
	PausedAccounts     []int     `json:"paused_account_ids"`
	Mappings           []Mapping `json:"mappings"`
	Issues             []Issue   `json:"issues"`
	Blockers           int       `json:"blockers"`
}

// Conversion holds secret-bearing write candidates in memory only. JSON output
// exposes the safe report, never credentials, channel keys, headers or history.
// Ready is not permission to migrate a live database; P5.2 must stop old writers,
// back up, verify target collisions and apply stages atomically with the journal.
type Conversion struct {
	Report            ConversionReport             `json:"report"`
	Fingerprint       string                       `json:"-"`
	Sites             []model.CheckinSite          `json:"-"`
	Accounts          []model.CheckinAccount       `json:"-"`
	Credentials       []model.CheckinCredential    `json:"-"`
	Channels          []model.Channel              `json:"-"`
	ChannelKeys       []model.ChannelKey           `json:"-"`
	Groups            []model.Group                `json:"-"`
	GroupItems        []model.GroupItem            `json:"-"`
	GroupPresets      []model.GroupPreset          `json:"-"`
	ChannelStats      []model.StatsChannel         `json:"-"`
	ArchivedSiteStats []model.StatsSiteModelHourly `json:"-"`
	Logs              []model.CheckinLog           `json:"-"`
	Batches           []model.CheckinBatchJob      `json:"-"`
	RetiredBindingIDs []int                        `json:"-"`
}

func (c *Conversion) Ready() bool { return c != nil && c.Report.Blockers == 0 }

func (c *Conversion) issue(severity, code, entity string, id, related int) {
	c.Report.Issues = append(c.Report.Issues, Issue{Severity: severity, Code: code, Entity: entity, ID: id, RelatedID: related})
}

func (c *Conversion) mapID(entity Entity, source, target int) {
	c.Report.Mappings = append(c.Report.Mappings, Mapping{entity, source, target})
}

func (c *Conversion) finishReport() {
	slices.SortFunc(c.Report.Issues, func(a, b Issue) int {
		if a.Entity != b.Entity {
			return compareString(a.Entity, b.Entity)
		}
		if a.ID != b.ID {
			return compareID(a.ID, b.ID)
		}
		if a.Code != b.Code {
			return compareString(a.Code, b.Code)
		}
		return compareID(a.RelatedID, b.RelatedID)
	})
	c.Report.Issues = slices.Compact(c.Report.Issues)
	c.Report.Blockers = 0
	for _, issue := range c.Report.Issues {
		if issue.Severity == "blocker" {
			c.Report.Blockers++
		}
	}
	slices.SortFunc(c.Report.Mappings, func(a, b Mapping) int {
		if a.Entity != b.Entity {
			return compareString(string(a.Entity), string(b.Entity))
		}
		return compareID(a.SourceID, b.SourceID)
	})
	c.Report.Mappings = slices.Compact(c.Report.Mappings)
	c.Report.PausedAccounts = sortedIDs(c.Report.PausedAccounts)
	c.Report.Sites, c.Report.Accounts, c.Report.Credentials = len(c.Sites), len(c.Accounts), len(c.Credentials)
	c.Report.ConvertedChannels, c.Report.Logs = len(c.RetiredBindingIDs), len(c.Logs)
}

func compareString(a, b string) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

func inputFingerprint(input *LegacyInput) (string, error) {
	hash := sha256.New()
	encoder := json.NewEncoder(hash)
	if err := encoder.Encode(input); err != nil {
		return "", errors.New("invalid conversion input")
	}
	// These runtime fields are omitted from v1 JSON. Include them for database
	// snapshots so a newly started token exchange cannot pass a stale-input check.
	for _, account := range input.Configuration.SiteAccounts {
		if err := encoder.Encode([]any{account.ID, account.TokenRefreshedAt, account.TokenRefreshLease, account.TokenRefreshLeaseUntil}); err != nil {
			return "", errors.New("invalid conversion session state")
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func clonePointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneStatus(value *model.SiteCheckinStatusConfig) *model.SiteCheckinStatusConfig {
	copy := clonePointer(value)
	if copy != nil {
		copy.Headers = slices.Clone(value.Headers)
		copy.CheckedValue, copy.UncheckedValue = slices.Clone(value.CheckedValue), slices.Clone(value.UncheckedValue)
	}
	return copy
}
