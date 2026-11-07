package op

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
)

// 日志列表的 total 计算。
//
// v0.9.12 把日志列表从 cursor 分页改成页码分页后，UI 固定带 with_total=true，
// 于是每翻一页就执行一次 COUNT(*)。relay_logs 是全库最大的表，无界 COUNT 即使
// 走 idx_relay_logs_time_id 也要扫全索引；SQLite 下写连接只有一个，这些慢查询
// 会和日志落库互相排队。
//
// 这里做两件事：
//  1. 有界计数 —— 数到 relayLogTotalMaxExact + 1 就停，UI 显示「10000+」；
//  2. 按 filter 签名缓存 —— 翻页时不再重复 COUNT。
const (
	relayLogTotalCacheTTL        = 5 * time.Second
	relayLogTotalCacheMaxEntries = 256
	relayLogTotalMaxExact        = 10000
)

type relayLogTotalCacheEntry struct {
	total     int
	exact     bool
	expiresAt time.Time
}

var (
	relayLogTotalCacheLock sync.Mutex
	relayLogTotalCache     = make(map[string]relayLogTotalCacheEntry)
)

// relayLogDBTotal 返回匹配 filter 的已落库日志条数。
// exact 为 false 表示实际条数大于 total（命中了 relayLogTotalMaxExact 上限）。
func relayLogDBTotal(ctx context.Context, filter RelayLogListFilter) (int, bool, error) {
	key := relayLogTotalCacheKey(filter)
	now := time.Now()

	if entry, ok := relayLogTotalCacheGet(key, now); ok {
		return entry.total, entry.exact, nil
	}

	total, exact, err := relayLogDBCountBounded(ctx, filter, relayLogTotalMaxExact)
	if err != nil {
		return 0, false, err
	}

	relayLogTotalCacheSet(key, relayLogTotalCacheEntry{
		total:     total,
		exact:     exact,
		expiresAt: now.Add(relayLogTotalCacheTTL),
	})
	return total, exact, nil
}

// relayLogDBCountBounded 生成
//
//	SELECT COUNT(*) FROM (SELECT 1 FROM relay_logs WHERE ... LIMIT n+1)
//
// 数到 n+1 条就停，把深表 COUNT 的代价压成常数。
func relayLogDBCountBounded(ctx context.Context, filter RelayLogListFilter, limit int) (int, bool, error) {
	inner := db.GetDB().WithContext(ctx).Model(&model.RelayLog{}).Select("1")
	inner = applyRelayLogDBFilters(inner, filter)
	inner = inner.Limit(limit + 1)

	var count int64
	if err := db.GetDB().WithContext(ctx).
		Table("(?) AS bounded_relay_logs", inner).
		Count(&count).Error; err != nil {
		return 0, false, err
	}

	if count > int64(limit) {
		return limit, false, nil
	}
	return int(count), true, nil
}

// RelayLogTotalCacheInvalidate 在日志被清理/删除后丢弃缓存的 total。
func RelayLogTotalCacheInvalidate() {
	relayLogTotalCacheLock.Lock()
	relayLogTotalCache = make(map[string]relayLogTotalCacheEntry)
	relayLogTotalCacheLock.Unlock()
}

func relayLogTotalCacheGet(key string, now time.Time) (relayLogTotalCacheEntry, bool) {
	relayLogTotalCacheLock.Lock()
	defer relayLogTotalCacheLock.Unlock()

	entry, ok := relayLogTotalCache[key]
	if !ok {
		return relayLogTotalCacheEntry{}, false
	}
	if now.After(entry.expiresAt) {
		delete(relayLogTotalCache, key)
		return relayLogTotalCacheEntry{}, false
	}
	return entry, true
}

func relayLogTotalCacheSet(key string, entry relayLogTotalCacheEntry) {
	relayLogTotalCacheLock.Lock()
	defer relayLogTotalCacheLock.Unlock()

	// 条目本来就只活 5 秒，撑满时直接整体清空比 LRU 便宜得多。
	if len(relayLogTotalCache) >= relayLogTotalCacheMaxEntries {
		relayLogTotalCache = make(map[string]relayLogTotalCacheEntry, relayLogTotalCacheMaxEntries)
	}
	relayLogTotalCache[key] = entry
}

// relayLogTotalCacheKey 只包含影响 applyRelayLogDBFilters 的字段 —— 页码、
// 每页大小、是否包含正文都不改变匹配集合。
func relayLogTotalCacheKey(filter RelayLogListFilter) string {
	var sb strings.Builder

	writeOptInt := func(p *int) {
		if p == nil {
			sb.WriteString("-")
		} else {
			sb.WriteString(strconv.Itoa(*p))
		}
		sb.WriteByte('|')
	}
	writeOptInt(filter.StartTime)
	writeOptInt(filter.EndTime)

	channelIDs := append([]int(nil), filter.ChannelIDs...)
	sort.Ints(channelIDs)
	for _, id := range channelIDs {
		sb.WriteString(strconv.Itoa(id))
		sb.WriteByte(',')
	}
	sb.WriteByte('|')

	sb.WriteString(string(filter.Status))
	sb.WriteByte('|')
	sb.WriteString(string(filter.KeywordMode))
	sb.WriteByte('|')
	sb.WriteString(string(filter.KeywordScope))
	sb.WriteByte('|')
	sb.WriteString(strings.ToLower(strings.TrimSpace(filter.Keyword)))

	return sb.String()
}
