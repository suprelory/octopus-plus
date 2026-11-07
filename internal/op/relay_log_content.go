package op

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"unicode/utf8"

	"github.com/suprelory/octopus-plus/internal/db"
	"github.com/suprelory/octopus-plus/internal/model"
	"gorm.io/gorm"
)

const relayLogContentQueueBytes = 64 << 20

func relayTraceBytes(trace *model.RelayTrace) int64 {
	if trace == nil {
		return 0
	}
	var total int64
	count := func(e model.RelayExchange) {
		for _, m := range []*model.RelayMessage{e.Request, e.Response} {
			if m != nil {
				total += int64(len(m.Data))
			}
		}
	}
	count(trace.Client)
	for _, e := range trace.Attempts {
		count(e)
	}
	return total
}

// Copy before dropping bytes: queued rows, SSE subscribers and recent entries
// must not mutate each other's capture metadata.
func relayTraceWithoutBodies(trace *model.RelayTrace, reason string) *model.RelayTrace {
	if trace == nil {
		return nil
	}
	copyMessage := func(m *model.RelayMessage) *model.RelayMessage {
		if m == nil {
			return nil
		}
		result := *m
		result.Data, result.Body = nil, ""
		if reason != "" && result.State != "not_captured" {
			result.State, result.Reason = "unavailable", reason
		}
		return &result
	}
	copyExchange := func(e model.RelayExchange) model.RelayExchange {
		e.Request, e.Response = copyMessage(e.Request), copyMessage(e.Response)
		return e
	}
	result := *trace
	result.Client = copyExchange(trace.Client)
	result.Attempts = make([]model.RelayExchange, len(trace.Attempts))
	for i, e := range trace.Attempts {
		result.Attempts[i] = copyExchange(e)
	}
	return &result
}

func persistRelayContents(tx *gorm.DB, logs []model.RelayLog) error {
	clients := make([]model.RelayRequestContent, 0)
	attempts := make([]model.RelayAttemptContent, 0)
	data := func(m *model.RelayMessage) []byte {
		if m == nil {
			return nil
		}
		return m.Data
	}
	for _, log := range logs {
		if log.Trace == nil {
			continue
		}
		metadata, err := json.Marshal(model.RelayTrace{ID: log.Trace.ID, ServingAttemptID: log.Trace.ServingAttemptID, Client: log.Trace.Client})
		if err != nil {
			return err
		}
		clients = append(clients, model.RelayRequestContent{LogID: log.ID, CreatedAt: log.Time, Metadata: string(metadata), RequestBody: data(log.Trace.Client.Request), ResponseBody: data(log.Trace.Client.Response)})
		for _, attempt := range log.Trace.Attempts {
			metadata, err := json.Marshal(attempt)
			if err != nil {
				return err
			}
			attempts = append(attempts, model.RelayAttemptContent{LogID: log.ID, AttemptID: attempt.AttemptID, CreatedAt: log.Time, Metadata: string(metadata), RequestBody: data(attempt.Request), ResponseBody: data(attempt.Response)})
		}
	}
	if len(clients) > 0 {
		if err := tx.CreateInBatches(&clients, relayLogBatchSize).Error; err != nil {
			return err
		}
	}
	if len(attempts) > 0 {
		return tx.CreateInBatches(&attempts, relayLogBatchSize).Error
	}
	return nil
}

func markExpired(e *model.RelayExchange) {
	for _, m := range []*model.RelayMessage{e.Request, e.Response} {
		if m != nil && m.State != "not_captured" && m.State != "unavailable" {
			m.State, m.Reason = "expired", "retention"
		}
	}
}

func loadRelayTraceMetadata(ctx context.Context, entry *model.RelayLog) error {
	var client model.RelayRequestContent
	result := db.GetDB().WithContext(ctx).Select("log_id", "metadata", "bodies_expired").Where("log_id = ?", entry.ID).Find(&client)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return nil
	}
	trace := new(model.RelayTrace)
	if err := json.Unmarshal([]byte(client.Metadata), trace); err != nil {
		return err
	}
	if client.BodiesExpired {
		markExpired(&trace.Client)
	}
	var attempts []model.RelayAttemptContent
	if err := db.GetDB().WithContext(ctx).Select("attempt_id", "metadata", "bodies_expired").Where("log_id = ?", entry.ID).Order("length(attempt_id), attempt_id").Find(&attempts).Error; err != nil {
		return err
	}
	trace.Attempts = make([]model.RelayExchange, 0, len(attempts))
	for _, row := range attempts {
		var attempt model.RelayExchange
		if err := json.Unmarshal([]byte(row.Metadata), &attempt); err != nil {
			return err
		}
		if row.BodiesExpired {
			markExpired(&attempt)
		}
		trace.Attempts = append(trace.Attempts, attempt)
	}
	entry.Trace = trace
	return nil
}

// RelayLogContentGet reads only the selected direction of one exchange.
func RelayLogContentGet(ctx context.Context, id int64, attemptID, direction string) (*model.RelayMessage, error) {
	if direction != "request" && direction != "response" {
		return nil, fmt.Errorf("invalid content direction")
	}
	var exchange model.RelayExchange
	var compressed []byte
	if pending, ok := relayLogFindPending(id); ok && pending.Trace != nil {
		exchange = pending.Trace.Client
		if attemptID != "" {
			found := false
			for _, e := range pending.Trace.Attempts {
				if e.AttemptID == attemptID {
					exchange, found = e, true
					break
				}
			}
			if !found {
				return nil, gorm.ErrRecordNotFound
			}
		}
		m := exchange.Request
		if direction == "response" {
			m = exchange.Response
		}
		if m != nil {
			compressed = m.Data
		}
	} else {
		column := direction + "_body"
		if attemptID == "" {
			var row model.RelayRequestContent
			if err := db.GetDB().WithContext(ctx).Select("metadata", "bodies_expired", column).Where("log_id = ?", id).First(&row).Error; err != nil {
				return nil, err
			}
			var trace model.RelayTrace
			if err := json.Unmarshal([]byte(row.Metadata), &trace); err != nil {
				return nil, err
			}
			exchange = trace.Client
			compressed = row.RequestBody
			if direction == "response" {
				compressed = row.ResponseBody
			}
			if row.BodiesExpired {
				markExpired(&exchange)
			}
		} else {
			var row model.RelayAttemptContent
			if err := db.GetDB().WithContext(ctx).Select("metadata", "bodies_expired", column).Where("log_id = ? AND attempt_id = ?", id, attemptID).First(&row).Error; err != nil {
				return nil, err
			}
			if err := json.Unmarshal([]byte(row.Metadata), &exchange); err != nil {
				return nil, err
			}
			compressed = row.RequestBody
			if direction == "response" {
				compressed = row.ResponseBody
			}
			if row.BodiesExpired {
				markExpired(&exchange)
			}
		}
	}
	m := exchange.Request
	if direction == "response" {
		m = exchange.Response
	}
	if m == nil {
		return &model.RelayMessage{State: "not_captured", Reason: "no_message"}, nil
	}
	result := *m
	result.Data = nil
	if len(compressed) == 0 {
		return &result, nil
	}
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	body, err := io.ReadAll(io.LimitReader(reader, (64<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(body) > 64<<20 {
		return nil, fmt.Errorf("captured body exceeds read limit")
	}
	if utf8.Valid(body) {
		result.Body = string(body)
		result.BodyEncoding = "utf-8"
		if direction == "response" {
			result.RawUsage = extractRelayRawUsage(body, result.ContentType)
		}
	} else {
		result.Body = base64.StdEncoding.EncodeToString(body)
		result.BodyEncoding = "base64"
	}
	return &result, nil
}

func deleteRelayLogRows(tx *gorm.DB, ids []int64) (int64, error) {
	var count int64
	err := tx.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("log_id IN ?", ids).Delete(&model.RelayAttemptContent{}).Error; err != nil {
			return err
		}
		if err := tx.Where("log_id IN ?", ids).Delete(&model.RelayRequestContent{}).Error; err != nil {
			return err
		}
		result := tx.Where("id IN ?", ids).Unscoped().Delete(&model.RelayLog{})
		count = result.RowsAffected
		return result.Error
	})
	return count, err
}
