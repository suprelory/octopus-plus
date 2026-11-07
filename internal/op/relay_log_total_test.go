package op

import (
	"testing"

	dbpkg "github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
)

func TestRelayLogTotalCacheKeyIgnoresPagination(t *testing.T) {
	base := RelayLogListFilter{
		StartTime:  intPtr(100),
		EndTime:    intPtr(200),
		ChannelIDs: []int{2, 1},
		Status:     RelayLogStatusSuccess,
		Keyword:    "GPT",
		Page:       1,
		PageSize:   20,
	}

	// 翻页不改变匹配集合，key 必须相同 —— 否则缓存对翻页毫无帮助。
	other := base
	other.Page = 7
	other.PageSize = 50
	other.IncludeContent = true
	if relayLogTotalCacheKey(base) != relayLogTotalCacheKey(other) {
		t.Fatal("cache key must not depend on page/page_size/include_content")
	}

	// 渠道顺序不同但集合相同，key 也必须相同。
	reordered := base
	reordered.ChannelIDs = []int{1, 2}
	if relayLogTotalCacheKey(base) != relayLogTotalCacheKey(reordered) {
		t.Fatal("cache key must not depend on channel id ordering")
	}

	// 大小写/空白归一化后关键字相同。
	padded := base
	padded.Keyword = "  gpt  "
	if relayLogTotalCacheKey(base) != relayLogTotalCacheKey(padded) {
		t.Fatal("cache key must normalize keyword case and padding")
	}

	// 影响过滤条件的字段必须改变 key。
	for name, mutate := range map[string]func(*RelayLogListFilter){
		"start time":    func(f *RelayLogListFilter) { f.StartTime = intPtr(101) },
		"end time":      func(f *RelayLogListFilter) { f.EndTime = nil },
		"channels":      func(f *RelayLogListFilter) { f.ChannelIDs = []int{3} },
		"status":        func(f *RelayLogListFilter) { f.Status = RelayLogStatusError },
		"keyword":       func(f *RelayLogListFilter) { f.Keyword = "claude" },
		"keyword mode":  func(f *RelayLogListFilter) { f.KeywordMode = RelayLogKeywordModeContains },
		"keyword scope": func(f *RelayLogListFilter) { f.KeywordScope = RelayLogKeywordScopeContent },
	} {
		t.Run(name, func(t *testing.T) {
			changed := base
			mutate(&changed)
			if relayLogTotalCacheKey(base) == relayLogTotalCacheKey(changed) {
				t.Fatalf("cache key must change when %s changes", name)
			}
		})
	}
}

func TestRelayLogDBCountBounded(t *testing.T) {
	ctx := setupSiteOpTestDB(t)
	if err := settingRefreshCache(ctx); err != nil {
		t.Fatalf("settingRefreshCache failed: %v", err)
	}
	resetRelayLogStateForTest()

	rows := make([]model.RelayLog, 0, 25)
	for i := 0; i < 25; i++ {
		rows = append(rows, model.RelayLog{
			ID:               int64(1000 + i),
			Time:             int64(1000 + i),
			RequestModelName: "gpt-4o-mini",
			Success:          true,
		})
	}
	if err := dbpkg.GetDB().WithContext(ctx).Create(&rows).Error; err != nil {
		t.Fatalf("create relay logs failed: %v", err)
	}

	filter := RelayLogListFilter{Page: 1, PageSize: 10, WithTotal: true}

	// 上限之内：精确计数。
	total, exact, err := relayLogDBCountBounded(ctx, filter, 100)
	if err != nil {
		t.Fatalf("relayLogDBCountBounded failed: %v", err)
	}
	if !exact || total != 25 {
		t.Fatalf("bounded count under the limit = (%d, exact=%v), want (25, true)", total, exact)
	}

	// 正好等于上限仍然算精确。
	total, exact, err = relayLogDBCountBounded(ctx, filter, 25)
	if err != nil {
		t.Fatalf("relayLogDBCountBounded failed: %v", err)
	}
	if !exact || total != 25 {
		t.Fatalf("bounded count at the limit = (%d, exact=%v), want (25, true)", total, exact)
	}

	// 超过上限：只报下界。
	total, exact, err = relayLogDBCountBounded(ctx, filter, 10)
	if err != nil {
		t.Fatalf("relayLogDBCountBounded failed: %v", err)
	}
	if exact || total != 10 {
		t.Fatalf("bounded count over the limit = (%d, exact=%v), want (10, false)", total, exact)
	}
}

func TestRelayLogDBTotalUsesCache(t *testing.T) {
	ctx := setupSiteOpTestDB(t)
	if err := settingRefreshCache(ctx); err != nil {
		t.Fatalf("settingRefreshCache failed: %v", err)
	}
	resetRelayLogStateForTest()

	rows := []model.RelayLog{
		{ID: 2001, Time: 2001, RequestModelName: "gpt-4o-mini", Success: true},
		{ID: 2002, Time: 2002, RequestModelName: "gpt-4o-mini", Success: true},
	}
	if err := dbpkg.GetDB().WithContext(ctx).Create(&rows).Error; err != nil {
		t.Fatalf("create relay logs failed: %v", err)
	}

	filter := RelayLogListFilter{Page: 1, PageSize: 10, WithTotal: true}

	total, exact, err := relayLogDBTotal(ctx, filter)
	if err != nil {
		t.Fatalf("relayLogDBTotal failed: %v", err)
	}
	if !exact || total != 2 {
		t.Fatalf("first total = (%d, exact=%v), want (2, true)", total, exact)
	}

	// 再插一行：TTL 内应该拿到缓存值，说明翻页不会重复 COUNT。
	if err := dbpkg.GetDB().WithContext(ctx).Create(&model.RelayLog{
		ID: 2003, Time: 2003, RequestModelName: "gpt-4o-mini", Success: true,
	}).Error; err != nil {
		t.Fatalf("create relay log failed: %v", err)
	}

	cachedTotal, _, err := relayLogDBTotal(ctx, filter)
	if err != nil {
		t.Fatalf("relayLogDBTotal failed: %v", err)
	}
	if cachedTotal != 2 {
		t.Fatalf("second total = %d, want the cached 2", cachedTotal)
	}

	// 失效后重新计数。
	RelayLogTotalCacheInvalidate()
	freshTotal, _, err := relayLogDBTotal(ctx, filter)
	if err != nil {
		t.Fatalf("relayLogDBTotal failed: %v", err)
	}
	if freshTotal != 3 {
		t.Fatalf("total after invalidation = %d, want 3", freshTotal)
	}
}
