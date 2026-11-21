package op

import (
	"errors"

	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/gorm"
)

var ErrBackupConflict = errors.New("backup configuration conflicts with existing data")

type BackupConfigurationMappings struct {
	Channels    map[int]int
	ChannelKeys map[int]int
	Proxies     map[int]int
	Groups      map[int]int
	APIKeys     map[int]int
}

// ImportBackupConfiguration restores the ordinary configuration and statistics
// within the caller's transaction. Version 2 supplies converted channels and
// imports its check-in history separately using the same transaction.
func ImportBackupConfiguration(tx *gorm.DB, dump *model.LegacyDBDump, result *model.DBImportResult) (*BackupConfigurationMappings, error) {
	state := newDBImportState(tx, dump, result)
	for _, stage := range []func() error{
		state.importProxies, state.importChannels, state.importChannelKeys,
		state.importGroups, state.importGroupItems, state.importGroupPresets,
		state.importModelPrices, state.importAPIKeys, state.importSettings, state.importStats,
	} {
		if err := stage(); err != nil {
			return nil, err
		}
	}
	return &BackupConfigurationMappings{Channels: state.channelIDs, ChannelKeys: state.channelKeyIDs, Proxies: state.proxyIDs, Groups: state.groupIDs, APIKeys: state.apiKeyIDs}, nil
}
