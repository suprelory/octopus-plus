package balancer

import (
	"testing"

	"github.com/suprelory/octopus-plus/internal/model"
)

func TestIteratorRecordsSelectionMetadata(t *testing.T) {
	Reset()
	iter := NewIterator(model.Group{
		ID:    7,
		Mode:  model.GroupModeFailover,
		Items: []model.GroupItem{{ID: 1, ChannelID: 11, ModelName: "mapped", Priority: 7}},
	}, 3, "requested")
	if !iter.Next() {
		t.Fatal("expected a candidate")
	}
	span := iter.StartAttempt(11, 21, "channel")
	span.End(model.AttemptSuccess, 200, "")
	attempts := iter.Attempts()
	if len(attempts) != 1 {
		t.Fatalf("expected one attempt, got %d", len(attempts))
	}
	attempt := attempts[0]
	if attempt.SelectionReason != "normal" || attempt.SelectionStrategy != "failover" {
		t.Fatalf("unexpected selection metadata: %#v", attempt)
	}
	if attempt.CandidateCount != 1 {
		t.Fatalf("expected candidate count 1, got %d", attempt.CandidateCount)
	}
	if attempt.SelectionMetrics == nil || attempt.SelectionMetrics.BaseRank == nil || *attempt.SelectionMetrics.BaseRank != 0 {
		t.Fatalf("expected base rank in selection metrics, got %#v", attempt.SelectionMetrics)
	}
	if attempt.SelectionMetrics.Priority != 7 || attempt.SelectionMetrics.CompositeScore < attempt.SelectionMetrics.DynamicScore {
		t.Fatalf("unexpected health scoring metadata: %#v", attempt.SelectionMetrics)
	}
}
