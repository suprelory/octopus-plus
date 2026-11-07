// Package transformer connects Octopus services to the independent Polywire module.
package transformer

import (
	"github.com/suprelory/octopus-plus/internal/utils/log"
	"github.com/suprelory/octopus-plus/internal/utils/tokenizer"
	"github.com/suprelory/octopus-plus/polywire"
	"github.com/suprelory/octopus-plus/polywire/inbound"
	"github.com/suprelory/octopus-plus/polywire/model"
	"github.com/suprelory/octopus-plus/polywire/outbound"
)

// The host owns this Engine for the process lifetime so scoped signatures survive
// retries and follow-up requests. Each factory call still returns a new adapter.
var engine = polywire.New(polywire.Config{
	Logger:       hostLogger{},
	TokenCounter: tokenizer.CountTokens,
})

func Inbound(typ inbound.InboundType) model.Inbound     { return engine.Inbound(typ) }
func Outbound(typ outbound.OutboundType) model.Outbound { return engine.Outbound(typ) }

func PlanRequestForModel(req *model.InternalLLMRequest, effectiveModel string, typ outbound.OutboundType, passthrough bool) outbound.CapabilityDecision {
	return engine.PlanRequestForModel(req, effectiveModel, typ, passthrough)
}

func PrepareRequestForModel(req *model.InternalLLMRequest, effectiveModel string, typ outbound.OutboundType, passthrough bool) (outbound.CapabilityDecision, *outbound.PreparedRequest) {
	return engine.PrepareRequestForModel(req, effectiveModel, typ, passthrough)
}

// Forward on every call so host log reconfiguration also affects existing adapters.
type hostLogger struct{}

func (hostLogger) Warnf(format string, args ...any)     { log.Warnf(format, args...) }
func (hostLogger) Warnw(message string, fields ...any)  { log.Warnw(message, fields...) }
func (hostLogger) Debugw(message string, fields ...any) { log.Debugw(message, fields...) }
