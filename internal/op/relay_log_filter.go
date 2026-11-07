package op

import (
	"strings"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/gorm"
)

type RelayLogStatusFilter string

const (
	RelayLogStatusAll     RelayLogStatusFilter = ""
	RelayLogStatusSuccess RelayLogStatusFilter = "success"
	RelayLogStatusError   RelayLogStatusFilter = "error"
)

type RelayLogKeywordScope string

const (
	RelayLogKeywordScopeDefault RelayLogKeywordScope = ""
	RelayLogKeywordScopeContent RelayLogKeywordScope = "content"
)

type RelayLogCursor struct {
	Time int64 `json:"time"`
	ID   int64 `json:"id"`
}

type RelayLogKeywordMode string

const (
	RelayLogKeywordModeDefault  RelayLogKeywordMode = ""
	RelayLogKeywordModePrefix   RelayLogKeywordMode = "prefix"
	RelayLogKeywordModeExact    RelayLogKeywordMode = "exact"
	RelayLogKeywordModeContains RelayLogKeywordMode = "contains"
)

type RelayLogListFilter struct {
	StartTime      *int
	EndTime        *int
	ChannelIDs     []int
	Status         RelayLogStatusFilter
	Keyword        string
	KeywordScope   RelayLogKeywordScope
	KeywordMode    RelayLogKeywordMode
	Page           int
	PageSize       int
	IncludeContent bool
	WithTotal      bool
	Limit          int
	BeforeTime     *int64
	BeforeID       *int64
	// Pagination forces cursor or page mode. Empty defers to cursor when
	// limit/cursor fields are set, otherwise page mode.
	Pagination string
}

type RelayLogListResult struct {
	Logs  []model.RelayLog `json:"logs"`
	Total int              `json:"total"`
	// TotalExact 为 false 表示 Total 是下界（命中了有界计数上限），
	// UI 应显示成 "Total+" 而不是精确值。
	TotalExact bool            `json:"total_exact"`
	HasMore    bool            `json:"has_more"`
	NextCursor *RelayLogCursor `json:"next_cursor,omitempty"`
	SearchMode string          `json:"search_mode,omitempty"`
	Warning    string          `json:"warning,omitempty"`
}

const (
	relayLogKeywordContainsMinLen     = 3
	relayLogKeywordContainsMaxWindow  = int64(7 * 24 * 60 * 60)
	relayLogKeywordContainsDefaultWin = int64(24 * 60 * 60)
)

// ErrRelayLogContainsKeywordTooShort signals that a contains-mode keyword does
// not meet the minimum length requirement enforced by the backend.
var (
	ErrRelayLogContainsKeywordTooShort = &RelayLogFilterError{Code: "keyword_too_short", Message: "contains search requires keyword of at least 3 characters"}
	ErrRelayLogContainsWindowMissing   = &RelayLogFilterError{Code: "time_window_required", Message: "contains search requires an explicit time range"}
	ErrRelayLogContainsWindowTooWide   = &RelayLogFilterError{Code: "time_window_too_wide", Message: "contains search time window must be at most 7 days"}
)

type RelayLogFilterError struct {
	Code    string
	Message string
}

func (e *RelayLogFilterError) Error() string { return e.Message }

func relayLogMatchesFilter(relayLog model.RelayLog, filter RelayLogListFilter, channelSet map[int]struct{}, keyword string) bool {
	if filter.StartTime != nil && relayLog.Time < int64(*filter.StartTime) {
		return false
	}
	if filter.EndTime != nil && relayLog.Time > int64(*filter.EndTime) {
		return false
	}
	if len(channelSet) > 0 && !logMatchesChannels(relayLog, channelSet) {
		return false
	}
	if filter.Status == RelayLogStatusSuccess && !relayLog.Success {
		return false
	}
	if filter.Status == RelayLogStatusError && relayLog.Success {
		return false
	}
	if keyword != "" && !logMatchesKeyword(relayLog, keyword, filter.KeywordScope, filter.KeywordMode) {
		return false
	}
	return true
}

func applyRelayLogDBFilters(query *gorm.DB, filter RelayLogListFilter) *gorm.DB {
	if filter.StartTime != nil {
		query = query.Where("time >= ?", *filter.StartTime)
	}
	if filter.EndTime != nil {
		query = query.Where("time <= ?", *filter.EndTime)
	}
	if len(filter.ChannelIDs) > 0 {
		query = query.Where("channel_id IN ?", filter.ChannelIDs)
	}
	if filter.Status == RelayLogStatusSuccess {
		query = query.Where("success = ?", true)
	} else if filter.Status == RelayLogStatusError {
		query = query.Where("success = ?", false)
	}
	keyword := strings.ToLower(strings.TrimSpace(filter.Keyword))
	if keyword == "" {
		return query
	}
	switch filter.KeywordMode {
	case RelayLogKeywordModeExact:
		query = query.Where(
			"LOWER(request_model_name) = ? OR LOWER(actual_model_name) = ? OR LOWER(request_api_key_name) = ? OR LOWER(channel_name) = ?",
			keyword, keyword, keyword, keyword,
		)
	case RelayLogKeywordModeContains:
		escaped := escapeLikeKeyword(keyword)
		like := "%" + escaped + "%"
		if filter.KeywordScope == RelayLogKeywordScopeContent {
			query = query.Where(
				"LOWER(request_model_name) LIKE ? ESCAPE '#' OR LOWER(actual_model_name) LIKE ? ESCAPE '#' OR LOWER(request_api_key_name) LIKE ? ESCAPE '#' OR LOWER(channel_name) LIKE ? ESCAPE '#' OR LOWER(request_content) LIKE ? ESCAPE '#' OR LOWER(response_content) LIKE ? ESCAPE '#' OR LOWER(error) LIKE ? ESCAPE '#'",
				like, like, like, like, like, like, like,
			)
		} else {
			query = query.Where(
				"LOWER(request_model_name) LIKE ? ESCAPE '#' OR LOWER(actual_model_name) LIKE ? ESCAPE '#' OR LOWER(request_api_key_name) LIKE ? ESCAPE '#' OR LOWER(channel_name) LIKE ? ESCAPE '#' OR LOWER(error) LIKE ? ESCAPE '#'",
				like, like, like, like, like,
			)
		}
	default:
		// prefix is the default fast path: anchored LIKE 'kw%' can leverage
		// indexes where available, and avoids the worst leading-wildcard scans.
		like := escapeLikeKeyword(keyword) + "%"
		query = query.Where(
			"LOWER(request_model_name) LIKE ? ESCAPE '#' OR LOWER(actual_model_name) LIKE ? ESCAPE '#' OR LOWER(request_api_key_name) LIKE ? ESCAPE '#' OR LOWER(channel_name) LIKE ? ESCAPE '#'",
			like, like, like, like,
		)
	}
	return query
}

// escapeLikeKeyword escapes SQL LIKE wildcards (and the escape char itself) so
// callers can match user input literally. Pair with `ESCAPE '#'` in the LIKE
// clause. A non-special ASCII char is used so the same SQL parses identically
// across SQLite, MySQL, and PostgreSQL string literals.
func escapeLikeKeyword(s string) string {
	s = strings.ReplaceAll(s, "#", "##")
	s = strings.ReplaceAll(s, "%", "#%")
	s = strings.ReplaceAll(s, "_", "#_")
	return s
}

// logMatchesChannels 检查日志是否属于指定的渠道集合。
// 仅匹配顶层 ChannelId，保持与 DB 查询 channel_id IN ? 一致，
// 避免缓存与 DB 分页/计数语义偏差。
func logMatchesChannels(log model.RelayLog, channelSet map[int]struct{}) bool {
	_, ok := channelSet[log.ChannelId]
	return ok
}

func logMatchesKeyword(relayLog model.RelayLog, keyword string, scope RelayLogKeywordScope, mode RelayLogKeywordMode) bool {
	fields := []string{
		relayLog.RequestModelName,
		relayLog.ActualModelName,
		relayLog.RequestAPIKeyName,
		relayLog.ChannelName,
	}
	if mode == RelayLogKeywordModeContains {
		fields = append(fields, relayLog.Error)
		if scope == RelayLogKeywordScopeContent {
			fields = append(fields, relayLog.RequestContent, relayLog.ResponseContent)
		}
	}
	for _, field := range fields {
		lower := strings.ToLower(field)
		switch mode {
		case RelayLogKeywordModeExact:
			if lower == keyword {
				return true
			}
		case RelayLogKeywordModeContains:
			if strings.Contains(lower, keyword) {
				return true
			}
		default:
			if strings.HasPrefix(lower, keyword) {
				return true
			}
		}
	}
	return false
}

// resolveRelayLogKeywordMode validates contains-mode constraints and returns
// the effective mode. Empty keyword always resolves to prefix to keep behavior
// stable for callers that don't care about mode.
func resolveRelayLogKeywordMode(filter *RelayLogListFilter) (RelayLogKeywordMode, string, error) {
	if filter.Keyword == "" {
		return RelayLogKeywordModeDefault, "", nil
	}
	mode := filter.KeywordMode
	if filter.KeywordScope == RelayLogKeywordScopeContent {
		// Content scope only makes sense with contains semantics.
		mode = RelayLogKeywordModeContains
	}
	switch mode {
	case RelayLogKeywordModePrefix, RelayLogKeywordModeExact, RelayLogKeywordModeDefault:
		if mode == RelayLogKeywordModeDefault {
			mode = RelayLogKeywordModePrefix
		}
		return mode, "", nil
	case RelayLogKeywordModeContains:
		if len([]rune(filter.Keyword)) < relayLogKeywordContainsMinLen {
			return mode, "", ErrRelayLogContainsKeywordTooShort
		}
		now := time.Now().Unix()
		warning := ""
		if filter.StartTime == nil && filter.EndTime == nil {
			// Apply a default 24h window rather than reject outright; surface
			// a warning so the UI can show it.
			start := int(now - relayLogKeywordContainsDefaultWin)
			filter.StartTime = &start
			warning = "applied default 24h time window for contains search"
		} else {
			end := now
			if filter.EndTime != nil {
				end = int64(*filter.EndTime)
			}
			var start int64
			if filter.StartTime != nil {
				start = int64(*filter.StartTime)
			} else {
				// EndTime set but StartTime not: anchor the window to EndTime
				// so end-only queries stay within the contains-search budget.
				start = end - relayLogKeywordContainsMaxWindow
				if start < 0 {
					start = 0
				}
				startInt := int(start)
				filter.StartTime = &startInt
			}
			if end-start > relayLogKeywordContainsMaxWindow {
				return mode, "", ErrRelayLogContainsWindowTooWide
			}
		}
		return mode, warning, nil
	default:
		return RelayLogKeywordModePrefix, "", nil
	}
}

func relayLogSearchMode(filter RelayLogListFilter) string {
	if filter.Keyword == "" {
		return ""
	}
	if filter.KeywordMode == RelayLogKeywordModeContains {
		return "slow"
	}
	return "fast"
}
