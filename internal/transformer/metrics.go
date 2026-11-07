package transformer

import (
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/suprelory/octopus-plus/polywire/model"
	"github.com/suprelory/octopus-plus/polywire/outbound"
)

type CapabilityMetricSnapshot struct {
	Total     uint64
	Supported uint64
	Degraded  uint64
	Rejected  uint64
	Losses    []LossMetric
}

type LossMetric struct {
	RequestType    model.RequestType
	InboundFormat  model.APIFormat
	OutboundFormat model.APIFormat
	Field          string
	Count          uint64
}

type lossMetricKey struct {
	requestType    model.RequestType
	inboundFormat  model.APIFormat
	outboundFormat model.APIFormat
	field          string
}

var capabilityMetrics struct {
	total     atomic.Uint64
	supported atomic.Uint64
	degraded  atomic.Uint64
	rejected  atomic.Uint64
	losses    sync.Map // lossMetricKey -> *atomic.Uint64
}

// RecordCapabilityDecision records aggregate quality without retaining request
// data. Octopus owns these counters; Polywire only returns conversion evidence.
func RecordCapabilityDecision(decision outbound.CapabilityDecision) {
	capabilityMetrics.total.Add(1)
	switch decision.Status {
	case outbound.CapabilitySupported:
		capabilityMetrics.supported.Add(1)
	case outbound.CapabilityDegraded:
		capabilityMetrics.degraded.Add(1)
	case outbound.CapabilityRejected:
		capabilityMetrics.rejected.Add(1)
	}
	seen := make(map[string]struct{}, len(decision.DegradedFields))
	for _, rawField := range decision.DegradedFields {
		field := strings.TrimSpace(normalizeMetricField(rawField))
		if field == "" {
			continue
		}
		if _, ok := seen[field]; ok {
			continue
		}
		seen[field] = struct{}{}
		key := lossMetricKey{decision.RequestType, decision.InboundFormat, decision.OutboundFormat, field}
		counter, _ := capabilityMetrics.losses.LoadOrStore(key, &atomic.Uint64{})
		counter.(*atomic.Uint64).Add(1)
	}
}

func normalizeMetricField(field string) string {
	var normalized strings.Builder
	normalized.Grow(len(field))
	for index := 0; index < len(field); {
		if field[index] != '[' {
			normalized.WriteByte(field[index])
			index++
			continue
		}
		end := index + 1
		for end < len(field) && field[end] >= '0' && field[end] <= '9' {
			end++
		}
		if end > index+1 && end < len(field) && field[end] == ']' {
			normalized.WriteString("[]")
			index = end + 1
			continue
		}
		normalized.WriteByte(field[index])
		index++
	}
	return normalized.String()
}

func SnapshotCapabilityMetrics() CapabilityMetricSnapshot {
	snapshot := CapabilityMetricSnapshot{
		Total: capabilityMetrics.total.Load(), Supported: capabilityMetrics.supported.Load(),
		Degraded: capabilityMetrics.degraded.Load(), Rejected: capabilityMetrics.rejected.Load(),
	}
	capabilityMetrics.losses.Range(func(key, value any) bool {
		metricKey := key.(lossMetricKey)
		snapshot.Losses = append(snapshot.Losses, LossMetric{
			RequestType: metricKey.requestType, InboundFormat: metricKey.inboundFormat,
			OutboundFormat: metricKey.outboundFormat, Field: metricKey.field, Count: value.(*atomic.Uint64).Load(),
		})
		return true
	})
	sort.Slice(snapshot.Losses, func(i, j int) bool {
		a, b := snapshot.Losses[i], snapshot.Losses[j]
		return strings.Join([]string{a.RequestType.String(), string(a.InboundFormat), string(a.OutboundFormat), a.Field}, "\x00") <
			strings.Join([]string{b.RequestType.String(), string(b.InboundFormat), string(b.OutboundFormat), b.Field}, "\x00")
	})
	return snapshot
}
