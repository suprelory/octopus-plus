package openai

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/suprelory/octopus-plus/polywire/model"
)

type StreamUpstreamError struct {
	Status  int
	Code    string
	Type    string
	Message string
	RetryAt time.Time
}

func (e *StreamUpstreamError) Error() string {
	if e == nil {
		return ""
	}
	parts := []string{}
	if e.Message != "" {
		parts = append(parts, e.Message)
	}
	if e.Code != "" {
		parts = append(parts, "code="+e.Code)
	}
	if e.Type != "" {
		parts = append(parts, "type="+e.Type)
	}
	if e.Status > 0 {
		parts = append(parts, fmt.Sprintf("status=%d", e.Status))
	}
	if len(parts) == 0 {
		return "upstream ws error"
	}
	return strings.Join(parts, ", ")
}

type ResponseEventObservation struct {
	Source           model.SourceEvent
	Type             string
	ResponseID       string
	Model            string
	Usage            *model.Usage
	RawOutput        json.RawMessage
	Terminal         bool
	FinishReasonSeen bool
	Error            *StreamUpstreamError
}

// InspectResponseEvent owns Responses envelope inspection for native transports.
// It leaves output items raw, preserving extensions for exact replay.
func InspectResponseEvent(data []byte, now time.Time) (ResponseEventObservation, error) {
	return InspectResponseSourceEvent(model.SourceEvent{Data: data, Transport: model.SourceTransportWebSocket}, now)
}

// InspectResponseSourceEvent shares its immutable wire DTO with canonical
// conversion. Transport observations must not advance the provider state.
func InspectResponseSourceEvent(source model.SourceEvent, now time.Time) (ResponseEventObservation, error) {
	source, event, err := parseResponseStreamEvent(source)
	if err != nil {
		return ResponseEventObservation{Source: source}, err
	}
	if source.Transport == model.SourceTransportWebSocket {
		source.Type = event.Type
		source.ID = responseStreamEventID(event)
	}
	result := ResponseEventObservation{Source: source, Type: event.Type, ResponseID: event.ID, Model: event.Model, Terminal: IsResponseTerminalEvent(event.Type)}
	if event.Usage != nil {
		result.Usage = convertResponsesUsage(event.Usage)
	}
	retryAt := ParseStreamRetryDeadline(now, event.RetryAfter, event.RetryAt)
	detail := event.Error
	if event.Response != nil {
		response := event.Response
		if response.ID != "" {
			result.ResponseID = response.ID
		}
		if response.Model != "" {
			result.Model = response.Model
		}
		if response.Usage != nil {
			result.Usage = convertResponsesUsage(response.Usage)
		}
		if len(response.RawOutput) > 0 && string(response.RawOutput) != "null" {
			result.RawOutput = response.RawOutput
		}
		status := ""
		if response.Status != nil {
			status = *response.Status
		}
		switch status {
		case "completed", "incomplete", "failed", "cancelled", "canceled":
			result.Terminal = true
			result.FinishReasonSeen = status == "completed" || status == "incomplete"
		}
		if response.Error != nil {
			detail = response.Error
			if deadline := ParseStreamRetryDeadline(now, response.RetryAfter, response.RetryAt); !deadline.IsZero() {
				retryAt = deadline
			}
		}
		if detail == nil && (status == "failed" || status == "cancelled" || status == "canceled") {
			detail = &ResponsesError{Message: "upstream response " + status}
		}
	}
	result.FinishReasonSeen = result.FinishReasonSeen || event.Type == "response.completed" || event.Type == "response.incomplete" || event.Type == "response.done"
	if detail != nil || IsResponseErrorEvent(event.Type) {
		status := event.Status
		if status < 400 {
			status = http.StatusBadGateway
		}
		result.Error = &StreamUpstreamError{Status: status, Code: NormalizeStreamErrorCode(event.Code), Message: event.Message, RetryAt: retryAt}
		if detail != nil {
			result.Error.Code, result.Error.Type, result.Error.Message = NormalizeStreamErrorCode(detail.Code), detail.Type, detail.Message
			if deadline := ParseStreamRetryDeadline(now, detail.RetryAfter, detail.RetryAt); !deadline.IsZero() {
				result.Error.RetryAt = deadline
			}
		}
		if result.Error.Message == "" {
			result.Error.Message = "upstream ws error"
		}
		result.Terminal = true
	}
	return result, nil
}

// RewriteModel avoids parsing and rewriting the frequent delta envelopes that
// inspection has already established contain no matching model field.
func (o ResponseEventObservation) RewriteModel(upstream, downstream string) []byte {
	if upstream == downstream {
		return o.Source.Data
	}
	if event, ok := o.Source.Decoded.(*ResponsesStreamEvent); ok {
		if event.Model != upstream && (event.Response == nil || event.Response.Model != upstream) {
			return o.Source.Data
		}
	}
	return RewriteResponseEventModel(o.Source.Data, upstream, downstream)
}

func NormalizeStreamErrorCode(code any) string {
	if code == nil {
		return ""
	}
	return fmt.Sprint(code)
}

func IsResponseTerminalEvent(eventType string) bool {
	switch strings.TrimSpace(eventType) {
	case "response.completed", "response.failed", "response.incomplete", "response.cancelled", "response.canceled", "response.error", "response.done":
		return true
	default:
		return false
	}
}

func IsResponseErrorEvent(eventType string) bool {
	switch strings.TrimSpace(eventType) {
	case "error", "response.failed", "response.error", "response.cancelled", "response.canceled":
		return true
	default:
		return false
	}
}

func ParseStreamRetryDeadline(now time.Time, retryAfter, retryAt json.RawMessage) time.Time {
	parse := func(raw json.RawMessage, absolute bool) time.Time {
		value := string(raw)
		var text string
		if json.Unmarshal(raw, &text) == nil {
			value = strings.TrimSpace(text)
		}
		if absolute {
			if deadline, err := time.Parse(time.RFC3339Nano, value); err == nil {
				return deadline
			}
		}
		if deadline, err := http.ParseTime(value); err == nil {
			return deadline
		}
		seconds, err := strconv.ParseFloat(value, 64)
		if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 || seconds >= float64(time.Duration(1<<63-1))/float64(time.Second) {
			return time.Time{}
		}
		return now.Add(time.Duration(seconds * float64(time.Second)))
	}
	if deadline := parse(retryAt, true); !deadline.IsZero() {
		return deadline
	}
	return parse(retryAfter, false)
}

func RewriteResponseEventModel(data []byte, upstream, downstream string) []byte {
	var payload map[string]json.RawMessage
	if json.Unmarshal(data, &payload) != nil {
		return data
	}
	changed := false
	rewrite := func(object map[string]json.RawMessage) {
		var value string
		if json.Unmarshal(object["model"], &value) == nil && value == upstream {
			object["model"], _ = json.Marshal(downstream)
			changed = true
		}
	}
	rewrite(payload)
	var response map[string]json.RawMessage
	if json.Unmarshal(payload["response"], &response) == nil && response != nil {
		rewrite(response)
		if changed {
			payload["response"], _ = json.Marshal(response)
		}
	}
	if changed {
		if encoded, err := json.Marshal(payload); err == nil {
			return encoded
		}
	}
	return data
}
