package balancer

import (
	"testing"

	"github.com/suprelory/octopus-plus/internal/model"
)

func TestGetBalancerFallsBackForRemovedRandomMode(t *testing.T) {
	if _, ok := GetBalancer(model.GroupMode(2)).(*RoundRobin); !ok {
		t.Fatalf("removed group mode 2 should fall back to round robin, got %T", GetBalancer(model.GroupMode(2)))
	}
}
