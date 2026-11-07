package transformer

import (
	"sync"
	"testing"

	"github.com/suprelory/octopus-plus/polywire/model"
	"github.com/suprelory/octopus-plus/polywire/outbound"
)

func TestCapabilityLossMetricsAreConcurrentAndFieldScoped(t *testing.T) {
	before := SnapshotCapabilityMetrics()
	const count = 64
	decision := outbound.CapabilityDecision{
		Status: outbound.CapabilityDegraded, RequestType: model.RequestTypeResponses,
		InboundFormat: model.APIFormatOpenAIResponse, OutboundFormat: model.APIFormatGeminiContents,
		DegradedFields: []string{"test.concurrent.loss", "test.concurrent.loss"},
	}
	var wg sync.WaitGroup
	for range count {
		wg.Add(1)
		go func() {
			defer wg.Done()
			RecordCapabilityDecision(decision)
		}()
	}
	wg.Wait()
	after := SnapshotCapabilityMetrics()
	if after.Total-before.Total != count || after.Degraded-before.Degraded != count {
		t.Fatalf("unexpected counter delta: before=%+v after=%+v", before, after)
	}
	var beforeLoss, afterLoss uint64
	for _, loss := range before.Losses {
		if loss.Field == "test.concurrent.loss" {
			beforeLoss = loss.Count
		}
	}
	for _, loss := range after.Losses {
		if loss.Field == "test.concurrent.loss" {
			afterLoss = loss.Count
		}
	}
	if afterLoss-beforeLoss != count {
		t.Fatalf("loss counter delta = %d, want %d", afterLoss-beforeLoss, count)
	}
}

func TestNormalizeMetricFieldRemovesUnboundedIndices(t *testing.T) {
	if got := normalizeMetricField("messages[123].content[456]"); got != "messages[].content[]" {
		t.Fatalf("normalized field = %q", got)
	}
	if got := normalizeMetricField("provider[field]"); got != "provider[field]" {
		t.Fatalf("non-numeric field changed to %q", got)
	}
}
