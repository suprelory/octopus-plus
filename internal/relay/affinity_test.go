package relay

import (
	"net/http"
	"strings"
	"testing"

	dbmodel "github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/relay/balancer"
)

func TestRequestAffinitySourceAndIsolation(t *testing.T) {
	_ = setupHTTPRelayTestDB(t)
	first := resolveRequestAffinity(http.Header{"X-Session-Id": []string{"session-a"}}, []byte(`{"session_id":"body-session","prompt_cache_key":"cache"}`))
	second := resolveRequestAffinity(http.Header{"X-Session-Id": []string{"session-b"}}, nil)
	if first.Source != "header" || first.Scope == second.Scope || strings.Contains(first.Scope, "session-a") {
		t.Fatalf("scopes=%+v %+v", first, second)
	}
	balancer.SetRoutingAffinity(1, 2, "model", 10, 11, first)
	if balancer.GetChannelAffinity(1, 2, "model", second) != nil || balancer.GetChannelAffinity(2, 2, "model", first) != nil || balancer.GetChannelAffinity(1, 3, "model", first) != nil || balancer.GetChannelAffinity(1, 2, "other", first) != nil {
		t.Fatal("affinity crossed a namespace")
	}
	if balancer.GetChannelAffinity(1, 2, "model", first) == nil {
		t.Fatal("affinity missing")
	}
	if err := op.SettingSetString(dbmodel.SettingKeyChannelAffinitySource, "prompt_cache_key"); err != nil {
		t.Fatal(err)
	}
	cache := resolveRequestAffinity(nil, []byte(`{"session_id":"body","prompt_cache_key":"cache"}`))
	if cache.Source != "prompt_cache_key" || cache.Scope == "" {
		t.Fatalf("cache=%+v", cache)
	}
	if missing := resolveRequestAffinity(nil, nil); missing.Mode != "off" {
		t.Fatalf("missing source=%+v", missing)
	}
	if err := op.SettingSetString(dbmodel.SettingKeyChannelAffinityMode, "off"); err != nil {
		t.Fatal(err)
	}
	if off := resolveRequestAffinity(nil, []byte(`{"prompt_cache_key":"cache"}`)); off.Mode != "off" || off.Scope != "" {
		t.Fatalf("off=%+v", off)
	}
}
