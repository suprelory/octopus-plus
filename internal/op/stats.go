package op

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/utils/cache"
	"github.com/suprelory/octopus-plus/internal/utils/log"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var statsDailyCache model.StatsDaily
var statsDailyCacheLock sync.RWMutex

var statsTotalCache model.StatsTotal
var statsTotalCacheLock sync.RWMutex

var statsHourlyCache [24]model.StatsHourly
var statsHourlyCacheLock sync.RWMutex

var statsChannelCache = cache.New[int, model.StatsChannel](16)
var statsChannelCacheNeedUpdate = make(map[int]struct{})
var statsChannelCacheNeedUpdateLock sync.Mutex

var statsAPIKeyCache = cache.New[int, model.StatsAPIKey](16)
var statsAPIKeyCacheNeedUpdate = make(map[int]struct{})
var statsAPIKeyCacheNeedUpdateLock sync.Mutex

// Serialize complete snapshot writes with key deletion, including day rollover.
var statsPersistenceLock sync.Mutex

// Restore holds this exclusively from its transaction through cache publication.
// Normal updates/flushes hold a read lock. Lock order: channelWriteLock,
// apiKeyWriteLock, statsLifecycleLock, statsPersistenceLock, individual caches.
var statsLifecycleLock sync.RWMutex

// pendingDailyOverrides holds prev-day StatsDaily snapshots whose persistence
// failed. Retried on the next StatsSaveDB cycle so a rollover snapshot is
// never silently dropped after the in-memory cache has advanced.
var pendingDailyOverrides []model.StatsDaily
var pendingDailyOverridesLock sync.Mutex

func enqueuePendingDailyOverride(d model.StatsDaily) {
	if d.Date == "" {
		return
	}
	pendingDailyOverridesLock.Lock()
	pendingDailyOverrides = append(pendingDailyOverrides, d)
	pendingDailyOverridesLock.Unlock()
}

func flushPendingDailyOverrides(ctx context.Context) error {
	pendingDailyOverridesLock.Lock()
	pending := pendingDailyOverrides
	pendingDailyOverrides = nil
	pendingDailyOverridesLock.Unlock()
	if len(pending) == 0 {
		return nil
	}
	dbConn := db.GetDB().WithContext(ctx)
	for i, p := range pending {
		if result := dbConn.Save(&p); result.Error != nil {
			pendingDailyOverridesLock.Lock()
			pendingDailyOverrides = append(pending[i:], pendingDailyOverrides...)
			pendingDailyOverridesLock.Unlock()
			return result.Error
		}
	}
	return nil
}

func StatsSaveDBTask() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	log.Debugf("stats save db task started")
	startTime := time.Now()
	defer func() {
		log.Debugf("stats save db task finished, save time: %s", time.Since(startTime))
	}()
	if err := StatsSaveDB(ctx); err != nil {
		log.Errorf("stats save db error: %v", err)
	}
	// Channel key runtime state is also deferred in memory. Persist it on every
	// statistics tick, even if another statistics table failed to save.
	if err := ChannelKeySaveDB(ctx); err != nil {
		log.Errorf("channel key save db error: %v", err)
	}
}

func StatsSaveDB(ctx context.Context) error {
	statsLifecycleLock.RLock()
	defer statsLifecycleLock.RUnlock()
	statsPersistenceLock.Lock()
	defer statsPersistenceLock.Unlock()
	if err := flushPendingDailyOverrides(ctx); err != nil {
		return err
	}

	statsTotalCacheLock.RLock()
	totalSnap := statsTotalCache
	statsTotalCacheLock.RUnlock()
	if totalSnap.ID == 0 {
		totalSnap.ID = 1
	}

	statsDailyCacheLock.RLock()
	dailySnap := statsDailyCache
	statsDailyCacheLock.RUnlock()

	statsHourlyCacheLock.RLock()
	hourlyAll := statsHourlyCache
	statsHourlyCacheLock.RUnlock()

	statsChannelCacheNeedUpdateLock.Lock()
	channelIDs := make([]int, 0, len(statsChannelCacheNeedUpdate))
	for id := range statsChannelCacheNeedUpdate {
		channelIDs = append(channelIDs, id)
	}
	statsChannelCacheNeedUpdate = make(map[int]struct{})
	statsChannelCacheNeedUpdateLock.Unlock()

	statsAPIKeyCacheNeedUpdateLock.Lock()
	apiKeyIDs := make([]int, 0, len(statsAPIKeyCacheNeedUpdate))
	for id := range statsAPIKeyCacheNeedUpdate {
		apiKeyIDs = append(apiKeyIDs, id)
	}
	statsAPIKeyCacheNeedUpdate = make(map[int]struct{})
	statsAPIKeyCacheNeedUpdateLock.Unlock()

	if err := persistStatsSnapshots(ctx, totalSnap, dailySnap, hourlyAll, channelIDs, apiKeyIDs); err != nil {
		restoreStatsDirtyIDs(channelIDs, apiKeyIDs)
		return err
	}
	return nil
}

func persistStatsSnapshots(
	ctx context.Context,
	totalSnap model.StatsTotal,
	dailySnap model.StatsDaily,
	hourlyAll [24]model.StatsHourly,
	channelIDs []int,
	apiKeyIDs []int,
) error {
	dbConn := db.GetDB().WithContext(ctx)
	if result := dbConn.Save(&totalSnap); result.Error != nil {
		return result.Error
	}
	if result := dbConn.Save(&dailySnap); result.Error != nil {
		return result.Error
	}

	todayDate := time.Now().Format("20060102")
	if err := persistStatsHourlySnapshot(dbConn, hourlyAll, todayDate); err != nil {
		return err
	}

	channelRows := make([]model.StatsChannel, 0, len(channelIDs))
	for _, id := range channelIDs {
		ch, ok := statsChannelCache.Get(id)
		if ok {
			channelRows = append(channelRows, ch)
		}
	}
	if len(channelRows) > 0 {
		if result := dbConn.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "channel_id"}},
			UpdateAll: true,
		}).CreateInBatches(&channelRows, 100); result.Error != nil {
			return result.Error
		}
	}

	apiKeyRows := make([]model.StatsAPIKey, 0, len(apiKeyIDs))
	for _, id := range apiKeyIDs {
		ak, ok := statsAPIKeyCache.Get(id)
		if ok {
			apiKeyRows = append(apiKeyRows, ak)
		}
	}
	if len(apiKeyRows) > 0 {
		if result := dbConn.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "api_key_id"}},
			UpdateAll: true,
		}).CreateInBatches(&apiKeyRows, 100); result.Error != nil {
			return result.Error
		}
	}

	if err := statsSiteModelHourlySaveDB(ctx); err != nil {
		return err
	}

	return nil
}

func persistStatsHourlySnapshot(dbConn *gorm.DB, hourlyAll [24]model.StatsHourly, todayDate string) error {
	hourlyStats := make([]model.StatsHourly, 0, 24)
	for hour := 0; hour < 24; hour++ {
		if hourlyAll[hour].Date == todayDate {
			hourlyStats = append(hourlyStats, hourlyAll[hour])
		}
	}
	if len(hourlyStats) == 0 {
		return nil
	}
	return dbConn.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "hour"}},
		UpdateAll: true,
	}).Create(&hourlyStats).Error
}

func statsSaveDBWithDailyOverride(ctx context.Context, dailyOverride model.StatsDaily) error {
	statsPersistenceLock.Lock()
	defer statsPersistenceLock.Unlock()
	statsTotalCacheLock.RLock()
	totalSnap := statsTotalCache
	statsTotalCacheLock.RUnlock()
	if totalSnap.ID == 0 {
		totalSnap.ID = 1
	}

	statsHourlyCacheLock.RLock()
	hourlyAll := statsHourlyCache
	statsHourlyCacheLock.RUnlock()

	statsChannelCacheNeedUpdateLock.Lock()
	channelIDs := make([]int, 0, len(statsChannelCacheNeedUpdate))
	for id := range statsChannelCacheNeedUpdate {
		channelIDs = append(channelIDs, id)
	}
	statsChannelCacheNeedUpdate = make(map[int]struct{})
	statsChannelCacheNeedUpdateLock.Unlock()

	statsAPIKeyCacheNeedUpdateLock.Lock()
	apiKeyIDs := make([]int, 0, len(statsAPIKeyCacheNeedUpdate))
	for id := range statsAPIKeyCacheNeedUpdate {
		apiKeyIDs = append(apiKeyIDs, id)
	}
	statsAPIKeyCacheNeedUpdate = make(map[int]struct{})
	statsAPIKeyCacheNeedUpdateLock.Unlock()

	if err := persistStatsSnapshots(ctx, totalSnap, dailyOverride, hourlyAll, channelIDs, apiKeyIDs); err != nil {
		restoreStatsDirtyIDs(channelIDs, apiKeyIDs)
		enqueuePendingDailyOverride(dailyOverride)
		return err
	}
	return nil
}

func restoreStatsDirtyIDs(channelIDs []int, apiKeyIDs []int) {
	if len(channelIDs) > 0 {
		statsChannelCacheNeedUpdateLock.Lock()
		for _, id := range channelIDs {
			statsChannelCacheNeedUpdate[id] = struct{}{}
		}
		statsChannelCacheNeedUpdateLock.Unlock()
	}
	if len(apiKeyIDs) > 0 {
		statsAPIKeyCacheNeedUpdateLock.Lock()
		for _, id := range apiKeyIDs {
			statsAPIKeyCacheNeedUpdate[id] = struct{}{}
		}
		statsAPIKeyCacheNeedUpdateLock.Unlock()
	}
}

func StatsDailyUpdate(ctx context.Context, metrics model.StatsMetrics) error {
	statsLifecycleLock.RLock()
	defer statsLifecycleLock.RUnlock()
	return statsDailyUpdate(ctx, metrics)
}

func statsDailyUpdate(ctx context.Context, metrics model.StatsMetrics) error {
	today := time.Now().Format("20060102")

	statsDailyCacheLock.Lock()
	if statsDailyCache.Date == today {
		statsDailyCache.StatsMetrics.Add(metrics)
		statsDailyCacheLock.Unlock()
		return nil
	}

	prevDaily := statsDailyCache
	statsDailyCache = model.StatsDaily{Date: today}
	statsDailyCache.StatsMetrics.Add(metrics)
	statsDailyCacheLock.Unlock()

	return statsSaveDBWithDailyOverride(ctx, prevDaily)
}

func StatsTotalUpdate(metrics model.StatsMetrics) error {
	statsLifecycleLock.RLock()
	defer statsLifecycleLock.RUnlock()
	return statsTotalUpdate(metrics)
}

func statsTotalUpdate(metrics model.StatsMetrics) error {
	statsTotalCacheLock.Lock()
	defer statsTotalCacheLock.Unlock()
	if statsTotalCache.ID == 0 {
		statsTotalCache.ID = 1
	}
	statsTotalCache.StatsMetrics.Add(metrics)
	return nil
}

func StatsChannelUpdate(channelID int, metrics model.StatsMetrics) error {
	statsLifecycleLock.RLock()
	defer statsLifecycleLock.RUnlock()
	return statsChannelUpdate(channelID, metrics)
}

func statsChannelUpdate(channelID int, metrics model.StatsMetrics) error {
	statsChannelCache.Update(channelID, func(current model.StatsChannel, exists bool) model.StatsChannel {
		if !exists {
			current.ChannelID = channelID
		}
		current.StatsMetrics.Add(metrics)
		return current
	})
	statsChannelCacheNeedUpdateLock.Lock()
	statsChannelCacheNeedUpdate[channelID] = struct{}{}
	statsChannelCacheNeedUpdateLock.Unlock()
	return nil
}

func StatsHourlyUpdate(metrics model.StatsMetrics) error {
	statsLifecycleLock.RLock()
	defer statsLifecycleLock.RUnlock()
	return statsHourlyUpdate(metrics)
}

func statsHourlyUpdate(metrics model.StatsMetrics) error {
	now := time.Now()
	nowHour := now.Hour()
	todayDate := time.Now().Format("20060102")

	statsHourlyCacheLock.Lock()
	defer statsHourlyCacheLock.Unlock()

	if statsHourlyCache[nowHour].Date != todayDate {
		statsHourlyCache[nowHour] = model.StatsHourly{
			Hour: nowHour,
			Date: todayDate,
		}
	}

	statsHourlyCache[nowHour].StatsMetrics.Add(metrics)
	return nil
}

func StatsAPIKeyUpdate(apiKeyID int, metrics model.StatsMetrics) error {
	apiKeyWriteLock.RLock()
	defer apiKeyWriteLock.RUnlock()
	statsLifecycleLock.RLock()
	defer statsLifecycleLock.RUnlock()
	return statsAPIKeyUpdate(apiKeyID, metrics)
}

func statsAPIKeyUpdate(apiKeyID int, metrics model.StatsMetrics) error {
	apiKeyCostLock.Lock()
	defer apiKeyCostLock.Unlock()
	// A request admitted before deletion may finish afterward. Global usage is
	// still recorded, but it must not recreate statistics for a deleted key.
	if _, exists := apiKeyCache.Get(apiKeyID); !exists {
		return nil
	}
	statsAPIKeyCache.Update(apiKeyID, func(current model.StatsAPIKey, exists bool) model.StatsAPIKey {
		if !exists {
			current.APIKeyID = apiKeyID
		}
		current.StatsMetrics.Add(metrics)
		return current
	})
	statsAPIKeyCacheNeedUpdateLock.Lock()
	statsAPIKeyCacheNeedUpdate[apiKeyID] = struct{}{}
	statsAPIKeyCacheNeedUpdateLock.Unlock()
	return nil
}

// StatsRecordRequest settles all usage dimensions on the same side of a restore.
// Attempt success/failure counters may already have been recorded separately.
func StatsRecordRequest(ctx context.Context, apiKeyID, channelID int, metrics model.StatsMetrics, includeChannelOutcome bool, attempts []model.ChannelAttempt, actualModel string) error {
	apiKeyWriteLock.RLock()
	defer apiKeyWriteLock.RUnlock()
	statsLifecycleLock.RLock()
	defer statsLifecycleLock.RUnlock()
	statsTotalUpdate(metrics)
	statsHourlyUpdate(metrics)
	statsAPIKeyUpdate(apiKeyID, metrics)
	if channelID != 0 {
		channelMetrics := metrics
		if !includeChannelOutcome {
			channelMetrics.WaitTime = 0
			channelMetrics.RequestSuccess = 0
			channelMetrics.RequestFailed = 0
		}
		statsChannelUpdate(channelID, channelMetrics)
	}
	statsSiteModelHourlyRecordAttempts(attempts, actualModel)
	return statsDailyUpdate(ctx, metrics)
}

func StatsChannelDel(id int) error {
	statsLifecycleLock.RLock()
	defer statsLifecycleLock.RUnlock()
	statsPersistenceLock.Lock()
	defer statsPersistenceLock.Unlock()
	if _, ok := statsChannelCache.Get(id); !ok {
		return nil
	}
	statsChannelCache.Del(id)
	statsChannelCacheNeedUpdateLock.Lock()
	delete(statsChannelCacheNeedUpdate, id)
	statsChannelCacheNeedUpdateLock.Unlock()
	return db.GetDB().Delete(&model.StatsChannel{}, id).Error
}

func StatsAPIKeyDel(id int) error {
	apiKeyWriteLock.Lock()
	defer apiKeyWriteLock.Unlock()
	statsLifecycleLock.RLock()
	defer statsLifecycleLock.RUnlock()
	statsPersistenceLock.Lock()
	defer statsPersistenceLock.Unlock()
	if err := db.GetDB().Where("api_key_id = ?", id).Delete(&model.StatsAPIKey{}).Error; err != nil {
		return err
	}
	clearAPIKeyStatsCache(id)
	return nil
}

func clearAPIKeyStatsCache(id int) {
	statsAPIKeyCache.Del(id)
	statsAPIKeyCacheNeedUpdateLock.Lock()
	delete(statsAPIKeyCacheNeedUpdate, id)
	statsAPIKeyCacheNeedUpdateLock.Unlock()
}

func StatsTotalGet() model.StatsTotal {
	statsTotalCacheLock.RLock()
	defer statsTotalCacheLock.RUnlock()
	return statsTotalCache
}

func StatsTodayGet() model.StatsDaily {
	statsDailyCacheLock.RLock()
	defer statsDailyCacheLock.RUnlock()
	return statsDailyCache
}

func StatsChannelGet(id int) model.StatsChannel {
	stats, ok := statsChannelCache.Get(id)
	if !ok {
		return model.StatsChannel{
			ChannelID: id,
		}
	}
	return stats
}

func StatsAPIKeyGet(id int) model.StatsAPIKey {
	statsLifecycleLock.RLock()
	defer statsLifecycleLock.RUnlock()
	return statsAPIKeyGet(id)
}

func statsAPIKeyGet(id int) model.StatsAPIKey {
	stats, ok := statsAPIKeyCache.Get(id)
	if !ok {
		return model.StatsAPIKey{
			APIKeyID: id,
		}
	}
	return stats
}

func StatsAPIKeyList() []model.StatsAPIKey {
	apiKeys := make([]model.StatsAPIKey, 0, statsAPIKeyCache.Len())
	for _, v := range statsAPIKeyCache.GetAll() {
		apiKeys = append(apiKeys, v)
	}
	return apiKeys
}

func StatsHourlyGet() []model.StatsHourly {
	now := time.Now()
	currentHour := now.Hour()
	todayDate := time.Now().Format("20060102")

	statsHourlyCacheLock.RLock()
	defer statsHourlyCacheLock.RUnlock()

	result := make([]model.StatsHourly, 0, currentHour+1)

	for hour := 0; hour <= currentHour; hour++ {
		if statsHourlyCache[hour].Date == todayDate {
			result = append(result, statsHourlyCache[hour])
		} else {
			result = append(result, model.StatsHourly{
				Hour: hour,
				Date: todayDate,
			})
		}
	}

	return result
}

func StatsGetDaily(ctx context.Context) ([]model.StatsDaily, error) {
	var statsDaily []model.StatsDaily
	result := db.GetDB().WithContext(ctx).Find(&statsDaily)
	if result.Error != nil {
		return nil, result.Error
	}
	return statsDaily, nil
}

func statsRefreshCache(ctx context.Context) error {
	statsLifecycleLock.Lock()
	defer statsLifecycleLock.Unlock()
	statsPersistenceLock.Lock()
	defer statsPersistenceLock.Unlock()
	snapshot, err := loadStatsCache(db.GetDB().WithContext(ctx))
	if err != nil {
		return err
	}
	snapshot.publish()
	return nil
}

// Load every table before publishing any cache. Import can load through its
// transaction and roll back on a query failure, then publish after commit.
type statsCacheSnapshot struct {
	daily    model.StatsDaily
	total    model.StatsTotal
	channels []model.StatsChannel
	hourly   []model.StatsHourly
	apiKeys  []model.StatsAPIKey
}

func loadStatsCache(dbConn *gorm.DB) (*statsCacheSnapshot, error) {
	today := time.Now().Format("20060102")

	var loadedDaily model.StatsDaily
	result := dbConn.Last(&loadedDaily)
	if result.Error != nil && !errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("failed to get daily stats: %w", result.Error)
	}
	if result.RowsAffected == 0 || loadedDaily.Date != today {
		loadedDaily = model.StatsDaily{Date: today}
	}

	var loadedTotal model.StatsTotal
	result = dbConn.First(&loadedTotal)
	if result.Error != nil && !errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("failed to get total stats: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		loadedTotal = model.StatsTotal{ID: 1}
	} else if loadedTotal.ID == 0 {
		loadedTotal.ID = 1
	}

	var loadedChannels []model.StatsChannel
	result = dbConn.Find(&loadedChannels)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to get channels: %w", result.Error)
	}

	var loadedHourly []model.StatsHourly
	result = dbConn.Find(&loadedHourly)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to get hourly stats: %w", result.Error)
	}
	var loadedAPIKeys []model.StatsAPIKey
	if err := dbConn.Find(&loadedAPIKeys).Error; err != nil {
		return nil, fmt.Errorf("failed to get api key stats: %w", err)
	}
	return &statsCacheSnapshot{loadedDaily, loadedTotal, loadedChannels, loadedHourly, loadedAPIKeys}, nil
}

// Requires statsLifecycleLock exclusively and statsPersistenceLock.
func (s *statsCacheSnapshot) publish() {
	statsDailyCacheLock.Lock()
	statsDailyCache = s.daily
	statsDailyCacheLock.Unlock()

	statsTotalCacheLock.Lock()
	statsTotalCache = s.total
	statsTotalCacheLock.Unlock()

	statsChannelCache.Clear()
	statsChannelCacheNeedUpdateLock.Lock()
	statsChannelCacheNeedUpdate = make(map[int]struct{})
	statsChannelCacheNeedUpdateLock.Unlock()
	for _, v := range s.channels {
		statsChannelCache.Set(v.ChannelID, v)
	}

	statsAPIKeyCache.Clear()
	statsAPIKeyCacheNeedUpdateLock.Lock()
	statsAPIKeyCacheNeedUpdate = make(map[int]struct{})
	statsAPIKeyCacheNeedUpdateLock.Unlock()
	for _, v := range s.apiKeys {
		statsAPIKeyCache.Set(v.APIKeyID, v)
	}

	statsHourlyCacheLock.Lock()
	statsHourlyCache = [24]model.StatsHourly{}
	for _, v := range s.hourly {
		if v.Hour >= 0 && v.Hour < 24 {
			statsHourlyCache[v.Hour] = v
		}
	}
	statsHourlyCacheLock.Unlock()

	pendingDailyOverridesLock.Lock()
	pendingDailyOverrides = nil
	pendingDailyOverridesLock.Unlock()
	siteModelHourlyCacheLock.Lock()
	siteModelHourlyCache = make(map[siteModelHourlyKey]*model.StatsSiteModelHourly)
	siteModelHourlyCacheLock.Unlock()
}
