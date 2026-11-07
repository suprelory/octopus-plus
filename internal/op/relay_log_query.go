package op

import (
	"context"
	"errors"
	"strings"

	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/gorm"
)

// RelayLogList 查询日志列表，支持可选的时间范围和渠道ID过滤
// startTime 和 endTime 为 nil 时表示不限制时间范围
// channelIDs 为 nil 或空时表示不限制渠道
func RelayLogList(ctx context.Context, startTime, endTime *int, channelIDs []int, page, pageSize int) ([]model.RelayLog, error) {
	result, err := RelayLogListWithFilter(ctx, RelayLogListFilter{
		StartTime:      startTime,
		EndTime:        endTime,
		ChannelIDs:     channelIDs,
		Page:           page,
		PageSize:       pageSize,
		IncludeContent: true,
		WithTotal:      true,
	})
	return result.Logs, err
}

// RelayLogListWithFilter 查询日志列表，支持时间、渠道、状态、关键字和 cursor 过滤。
func RelayLogListWithFilter(ctx context.Context, filter RelayLogListFilter) (RelayLogListResult, error) {
	var result RelayLogListResult
	var err error
	if filter.KeywordScope == RelayLogKeywordScopeContent && strings.TrimSpace(filter.Keyword) != "" {
		result, err = relayLogSearchContents(ctx, filter)
	} else {
		result, err = relayLogListWithFilter(ctx, filter)
	}
	if err == nil && filter.IncludeContent {
		for i := range result.Logs {
			entry := &result.Logs[i]
			if entry.Trace == nil {
				if err := loadRelayTraceMetadata(ctx, entry); err != nil {
					return RelayLogListResult{}, err
				}
			}
			if entry.Trace == nil {
				continue
			}
			request, reqErr := RelayLogContentGet(ctx, entry.ID, "", "request")
			response, respErr := RelayLogContentGet(ctx, entry.ID, "", "response")
			if reqErr == nil {
				entry.RequestContent = request.Body
			}
			if respErr == nil {
				entry.ResponseContent = response.Body
			}
			entry.Trace = relayTraceWithoutBodies(entry.Trace, "")
		}
	}
	return result, err
}

func relayLogListWithFilter(ctx context.Context, filter RelayLogListFilter) (RelayLogListResult, error) {
	enabled, err := SettingGetBool(model.SettingKeyRelayLogKeepEnabled)
	if err != nil {
		return RelayLogListResult{}, err
	}

	cursorMode := filter.BeforeTime != nil || filter.BeforeID != nil || filter.Limit > 0
	switch filter.Pagination {
	case "cursor":
		cursorMode = true
	case "page":
		cursorMode = false
	}
	if filter.Limit < 0 || filter.Limit > 100 {
		filter.Limit = 20
	}
	if filter.Limit == 0 {
		filter.Limit = filter.PageSize
	}
	if filter.Limit < 1 {
		filter.Limit = 20
	}

	if filter.Page < 1 {
		filter.Page = 1
	}
	if filter.PageSize < 1 || filter.PageSize > 100 {
		filter.PageSize = 20
	}
	filter.Keyword = strings.TrimSpace(filter.Keyword)

	// Resolve effective keyword mode and apply guardrails for slow contains
	// search before any DB work.
	resolvedMode, warning, err := resolveRelayLogKeywordMode(&filter)
	if err != nil {
		return RelayLogListResult{}, err
	}
	filter.KeywordMode = resolvedMode

	hasChannelFilter := len(filter.ChannelIDs) > 0
	var channelSet map[int]struct{}
	if hasChannelFilter {
		channelSet = make(map[int]struct{}, len(filter.ChannelIDs))
		for _, id := range filter.ChannelIDs {
			channelSet[id] = struct{}{}
		}
	}

	keyword := strings.ToLower(filter.Keyword)

	cachedLogs := relayLogCachedMatches(filter, channelSet, keyword, enabled, !filter.IncludeContent)

	cacheCount := len(cachedLogs)
	offset := (filter.Page - 1) * filter.PageSize

	if cursorMode {
		result, err := relayLogListCursor(ctx, filter, cachedLogs, enabled)
		if err != nil {
			return RelayLogListResult{}, err
		}
		// cursor 模式不报 total，标成 exact 以免前端把 0 当成下界渲染。
		result.TotalExact = true
		result.SearchMode = relayLogSearchMode(filter)
		result.Warning = warning
		return result, nil
	}

	var logs []model.RelayLog
	total := 0
	// Page mode reads one extra row so callers can keep navigating when Total is
	// only a lower bound. The extra row is removed before returning.
	pageFetchLimit := filter.PageSize + 1
	// 未落库的 pending 条目是精确计数；只有 DB 侧的有界计数会把它变成下界。
	totalExact := true
	if filter.WithTotal {
		total = cacheCount
	}

	// 先从未落库 pending 中取（pending 是最新日志）；已落库日志从 DB 取，避免重复分页。
	if offset < cacheCount {
		cacheEnd := offset + pageFetchLimit
		if cacheEnd > cacheCount {
			cacheEnd = cacheCount
		}
		logs = append(logs, cachedLogs[offset:cacheEnd]...)
	}

	// 如果启用了日志保存，从数据库读取剩余条目；仅按需统计总数。
	if enabled {
		if filter.WithTotal {
			dbCount, exact, err := relayLogDBTotal(ctx, filter)
			if err != nil {
				return RelayLogListResult{}, err
			}
			total += dbCount
			totalExact = exact
		}

		remaining := pageFetchLimit - len(logs)
		if remaining > 0 {
			dbOffset := 0
			if offset > cacheCount {
				dbOffset = offset - cacheCount
			}

			query := db.GetDB().WithContext(ctx)
			query = applyRelayLogDBFilters(query, filter)
			query = selectRelayLogListFields(query, filter.IncludeContent)

			var dbLogs []model.RelayLog
			if err := query.Order("time DESC").Order("id DESC").Offset(dbOffset).Limit(remaining).Find(&dbLogs).Error; err != nil {
				return RelayLogListResult{}, err
			}
			logs = appendDedupedByID(logs, cachedLogs, dbLogs)
		}
	}

	hasMore := len(logs) > filter.PageSize
	if hasMore {
		logs = logs[:filter.PageSize]
	}

	return RelayLogListResult{
		Logs:       logs,
		Total:      total,
		TotalExact: totalExact,
		HasMore:    hasMore,
		SearchMode: relayLogSearchMode(filter),
		Warning:    warning,
	}, nil
}

func relayLogListCursor(ctx context.Context, filter RelayLogListFilter, cachedLogs []model.RelayLog, enabled bool) (RelayLogListResult, error) {
	limit := filter.Limit
	if limit < 1 || limit > 100 {
		limit = 20
	}
	logs := make([]model.RelayLog, 0, limit)
	for _, entry := range cachedLogs {
		if !relayLogBeforeCursor(entry, filter.BeforeTime, filter.BeforeID) {
			continue
		}
		logs = append(logs, entry)
		if len(logs) >= limit+1 {
			break
		}
	}

	if enabled && len(logs) < limit+1 {
		remaining := limit + 1 - len(logs)
		query := db.GetDB().WithContext(ctx)
		query = applyRelayLogDBFilters(query, filter)
		query = applyRelayLogCursor(query, filter.BeforeTime, filter.BeforeID)
		query = selectRelayLogListFields(query, filter.IncludeContent)

		var dbLogs []model.RelayLog
		if err := query.Order("time DESC").Order("id DESC").Limit(remaining).Find(&dbLogs).Error; err != nil {
			return RelayLogListResult{}, err
		}
		logs = appendDedupedByID(logs, cachedLogs, dbLogs)
	}

	hasMore := len(logs) > limit
	if hasMore {
		logs = logs[:limit]
	}
	var nextCursor *RelayLogCursor
	if hasMore && len(logs) > 0 {
		last := logs[len(logs)-1]
		nextCursor = &RelayLogCursor{Time: last.Time, ID: last.ID}
	}
	return RelayLogListResult{Logs: logs, HasMore: hasMore, NextCursor: nextCursor}, nil
}

func relayLogBeforeCursor(entry model.RelayLog, beforeTime *int64, beforeID *int64) bool {
	if beforeTime == nil && beforeID == nil {
		return true
	}
	if beforeTime != nil && beforeID != nil {
		return entry.Time < *beforeTime || (entry.Time == *beforeTime && entry.ID < *beforeID)
	}
	if beforeTime != nil {
		return entry.Time < *beforeTime
	}
	return beforeID == nil || entry.ID < *beforeID
}

func applyRelayLogCursor(query *gorm.DB, beforeTime *int64, beforeID *int64) *gorm.DB {
	if beforeTime != nil && beforeID != nil {
		return query.Where("time < ? OR (time = ? AND id < ?)", *beforeTime, *beforeTime, *beforeID)
	}
	if beforeTime != nil {
		return query.Where("time < ?", *beforeTime)
	}
	if beforeID != nil {
		return query.Where("id < ?", *beforeID)
	}
	return query
}

func selectRelayLogListFields(query *gorm.DB, includeContent bool) *gorm.DB {
	if includeContent {
		return query
	}
	return query.Select(
		"id",
		"time",
		"request_model_name",
		"request_api_key_id",
		"request_api_key_name",
		"client_ip",
		"endpoint_type",
		"channel_id",
		"channel_name",
		"actual_model_name",
		"input_tokens",
		"transport_input_tokens",
		"bill_input_tokens",
		"cache_read_tokens",
		"cache_write_tokens",
		"output_tokens",
		"reasoning_effort",
		"reasoning_tokens",
		"reasoning_chars",
		"ftut",
		"use_time",
		"cost",
		"error",
		"success",
		"attempts",
		"total_attempts",
		"used_ws",
		"ws_mode",
		"ws_exec_mode",
		"ws_recovery",
	)
}

// appendDedupedByID appends dbLogs to logs, skipping any entry whose ID is
// already present in cachedSource. The cache snapshot and DB read are not
// transactionally consistent — a batch flushed between the two reads could
// otherwise surface in both, producing duplicate rows by ID.
func appendDedupedByID(logs []model.RelayLog, cachedSource []model.RelayLog, dbLogs []model.RelayLog) []model.RelayLog {
	if len(dbLogs) == 0 {
		return logs
	}
	if len(cachedSource) == 0 {
		return append(logs, dbLogs...)
	}
	seen := make(map[int64]struct{}, len(cachedSource))
	for _, entry := range cachedSource {
		seen[entry.ID] = struct{}{}
	}
	for _, entry := range dbLogs {
		if _, ok := seen[entry.ID]; ok {
			continue
		}
		logs = append(logs, entry)
	}
	return logs
}

func RelayLogGet(ctx context.Context, id int64) (*model.RelayLog, error) {
	if item, ok := relayLogFindPending(id); ok {
		item.Trace = relayTraceWithoutBodies(item.Trace, "")
		return &item, nil
	}
	var entry model.RelayLog
	if err := db.GetDB().WithContext(ctx).First(&entry, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if item, ok := relayLogFindRecent(id); ok {
				return &item, nil
			}
		}
		return nil, err
	}
	if err := loadRelayTraceMetadata(ctx, &entry); err != nil {
		return nil, err
	}
	return &entry, nil
}

func relayLogCachedMatches(filter RelayLogListFilter, channelSet map[int]struct{}, keyword string, enabled bool, light bool) []model.RelayLog {
	if enabled {
		return relayLogCollectPending(filter, channelSet, keyword, light)
	}
	return relayLogCollectRecent(filter, channelSet, keyword, light)
}

func relayLogCollectPending(filter RelayLogListFilter, channelSet map[int]struct{}, keyword string, light bool) []model.RelayLog {
	relayLogBuffer.pendingLock.Lock()
	defer relayLogBuffer.pendingLock.Unlock()
	result := make([]model.RelayLog, 0, min(len(relayLogBuffer.pending), filter.PageSize+filter.Limit+1))
	for i := len(relayLogBuffer.pending) - 1; i >= 0; i-- {
		entry := relayLogBuffer.pending[i]
		if !relayLogMatchesFilter(entry, filter, channelSet, keyword) {
			continue
		}
		if light {
			entry = relayLogLightCopy(entry)
		}
		result = append(result, entry)
	}
	return result
}

func relayLogCollectRecent(filter RelayLogListFilter, channelSet map[int]struct{}, keyword string, light bool) []model.RelayLog {
	relayLogBuffer.recentLock.Lock()
	defer relayLogBuffer.recentLock.Unlock()
	result := make([]model.RelayLog, 0, min(len(relayLogBuffer.recent), filter.PageSize+filter.Limit+1))
	for i := len(relayLogBuffer.recent) - 1; i >= 0; i-- {
		entry := relayLogBuffer.recent[i]
		if !relayLogMatchesFilter(entry, filter, channelSet, keyword) {
			continue
		}
		if light {
			entry = relayLogLightCopy(entry)
		}
		result = append(result, entry)
	}
	return result
}

func relayLogFindPending(id int64) (model.RelayLog, bool) {
	relayLogBuffer.pendingLock.Lock()
	defer relayLogBuffer.pendingLock.Unlock()
	for i := len(relayLogBuffer.pending) - 1; i >= 0; i-- {
		if relayLogBuffer.pending[i].ID == id {
			return relayLogBuffer.pending[i], true
		}
	}
	return model.RelayLog{}, false
}

func relayLogFindRecent(id int64) (model.RelayLog, bool) {
	relayLogBuffer.recentLock.Lock()
	defer relayLogBuffer.recentLock.Unlock()
	for i := len(relayLogBuffer.recent) - 1; i >= 0; i-- {
		if relayLogBuffer.recent[i].ID == id {
			return relayLogBuffer.recent[i], true
		}
	}
	return model.RelayLog{}, false
}

func relayLogLightCopy(entry model.RelayLog) model.RelayLog {
	entry.Trace = nil
	entry.RequestContent = ""
	entry.ResponseContent = ""
	return entry
}
