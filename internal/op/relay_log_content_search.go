package op

import (
	"context"
	"strings"

	"github.com/suprelory/octopus-plus/internal/model"
)

// Compressed bodies cannot participate in SQL LIKE. Scan a bounded set of
// candidates inside the existing time/channel filters and expose continuation
// through the cursor. Page searches must narrow their range when they exceed
// the budget, rather than returning a misleading total or silently missing rows.
func relayLogSearchContents(ctx context.Context, filter RelayLogListFilter) (RelayLogListResult, error) {
	filter.Keyword = strings.TrimSpace(filter.Keyword)
	mode, warning, err := resolveRelayLogKeywordMode(&filter)
	if err != nil {
		return RelayLogListResult{}, err
	}
	filter.KeywordMode = mode
	cursorMode := filter.Pagination == "cursor" || (filter.Pagination != "page" && (filter.BeforeTime != nil || filter.BeforeID != nil || filter.Limit > 0))
	size := filter.PageSize
	if cursorMode && filter.Limit > 0 {
		size = filter.Limit
	}
	if size < 1 || size > 100 {
		size = 20
	}
	page := max(filter.Page, 1)
	skip := (page - 1) * size
	if cursorMode {
		skip = 0
	}
	base := filter
	base.Keyword, base.KeywordScope, base.KeywordMode = "", "", ""
	base.Pagination, base.Limit, base.PageSize, base.WithTotal, base.IncludeContent = "cursor", 50, 50, false, true
	if !cursorMode {
		base.BeforeTime, base.BeforeID = nil, nil
	}
	result := RelayLogListResult{Logs: make([]model.RelayLog, 0, size), TotalExact: true, SearchMode: "slow", Warning: warning}
	remainingBytes := int64(128 << 20)
	scanned := 0
	var last *RelayLogCursor
	keyword := strings.ToLower(filter.Keyword)
	for {
		batch, err := relayLogListWithFilter(ctx, base)
		if err != nil {
			return result, err
		}
		for _, entry := range batch.Logs {
			if err := ctx.Err(); err != nil {
				return result, err
			}
			if scanned >= 500 || remainingBytes <= 0 {
				if !cursorMode || last == nil {
					return result, &RelayLogFilterError{Code: "content_search_too_broad", Message: "body search exceeded its scan budget; narrow the time range or channel filter"}
				}
				result.TotalExact, result.HasMore, result.NextCursor = false, true, last
				result.Warning = "body search reached its scan budget; continue with the next cursor"
				return result, nil
			}
			matched, err := relayLogContentMatches(ctx, &entry, filter, keyword, &remainingBytes)
			if err != nil {
				return result, err
			}
			scanned++
			last = &RelayLogCursor{Time: entry.Time, ID: entry.ID}
			if !matched {
				continue
			}
			result.Total++
			if result.Total <= skip {
				continue
			}
			if len(result.Logs) < size {
				if !filter.IncludeContent {
					entry = relayLogLightCopy(entry)
				}
				result.Logs = append(result.Logs, entry)
				if cursorMode && len(result.Logs) == size {
					result.HasMore, result.NextCursor, result.TotalExact = true, last, false
					return result, nil
				}
			} else {
				result.HasMore = true
			}
		}
		if !batch.HasMore || batch.NextCursor == nil {
			break
		}
		base.BeforeTime, base.BeforeID = &batch.NextCursor.Time, &batch.NextCursor.ID
	}
	return result, nil
}

func relayLogContentMatches(ctx context.Context, entry *model.RelayLog, filter RelayLogListFilter, keyword string, budget *int64) (bool, error) {
	*budget -= int64(len(entry.RequestContent) + len(entry.ResponseContent))
	if logMatchesKeyword(*entry, keyword, filter.KeywordScope, filter.KeywordMode) {
		return true, nil
	}
	if entry.Trace == nil {
		if err := loadRelayTraceMetadata(ctx, entry); err != nil {
			return false, err
		}
	}
	if entry.Trace == nil {
		return false, nil
	}
	exchanges := append([]model.RelayExchange{entry.Trace.Client}, entry.Trace.Attempts...)
	for _, exchange := range exchanges {
		for _, direction := range []string{"request", "response"} {
			m := exchange.Request
			if direction == "response" {
				m = exchange.Response
			}
			if m == nil || m.CapturedBytes == 0 || (m.State != "captured" && m.State != "partial" && m.State != "truncated") {
				continue
			}
			if m.CapturedBytes > *budget {
				return false, &RelayLogFilterError{Code: "content_search_too_broad", Message: "body search exceeded its byte budget; narrow the time range or channel filter"}
			}
			*budget -= m.CapturedBytes
			message, err := RelayLogContentGet(ctx, entry.ID, exchange.AttemptID, direction)
			if err != nil {
				return false, err
			}
			if message.BodyEncoding != "base64" && strings.Contains(strings.ToLower(message.Body), keyword) {
				return true, nil
			}
		}
	}
	return false, nil
}
