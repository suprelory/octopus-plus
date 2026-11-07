// Package polywire converts LLM requests, responses and streams between wire
// protocols. It constructs HTTP requests but never opens a network connection.
package polywire

import (
	"github.com/suprelory/octopus-plus/polywire/compat"
	"github.com/suprelory/octopus-plus/polywire/config"
	"github.com/suprelory/octopus-plus/polywire/inbound"
	"github.com/suprelory/octopus-plus/polywire/model"
	"github.com/suprelory/octopus-plus/polywire/outbound"
)

// Config defines the services shared by an Engine's adapters.
type Config = config.Config

// Logger receives optional conversion diagnostics.
type Logger = config.Logger

// TokenCounter estimates input tokens when provider usage is not yet available.
type TokenCounter = config.TokenCounter

// Engine owns shared conversion dependencies. It can be used concurrently;
// adapters returned by Inbound and Outbound belong to one request attempt or
// stream and must not be shared concurrently. Reuse an Engine across requests
// when scoped Gemini signature recovery is needed.
type Engine struct {
	config config.Config
}

// New creates an Engine with a private bounded signature store unless the caller
// supplies one. A nil logger is silent and a nil token counter disables estimation.
func New(cfg Config) *Engine {
	if cfg.SignatureStore == nil {
		cfg.SignatureStore = compat.NewMemorySignatureStore()
	}
	return &Engine{config: cfg}
}

// Inbound returns a fresh client-protocol adapter, or nil for an unknown type.
func (e *Engine) Inbound(typ inbound.InboundType) model.Inbound {
	return inbound.New(typ, e.config)
}

// Outbound returns a fresh provider-protocol adapter, or nil for an unknown type.
func (e *Engine) Outbound(typ outbound.OutboundType) model.Outbound {
	return outbound.New(typ, e.config)
}

// PlanRequestForModel evaluates a cloned request using the same dependencies as
// the execution adapters. It returns conversion evidence; policy enforcement,
// channel selection and metrics belong to the host.
func (e *Engine) PlanRequestForModel(req *model.InternalLLMRequest, effectiveModel string, typ outbound.OutboundType, passthrough bool) outbound.CapabilityDecision {
	return outbound.PlanRequestForModelWithConfig(req, effectiveModel, typ, passthrough, e.config)
}

// PrepareRequestForModel retains the validated conversion for execution with
// Build, avoiding another protocol conversion after channel and key selection.
func (e *Engine) PrepareRequestForModel(req *model.InternalLLMRequest, effectiveModel string, typ outbound.OutboundType, passthrough bool) (outbound.CapabilityDecision, *outbound.PreparedRequest) {
	return outbound.PrepareRequestForModelWithConfig(req, effectiveModel, typ, passthrough, e.config)
}
