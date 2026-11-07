package inbound

import (
	"sort"

	"github.com/suprelory/octopus-plus/polywire/config"
	"github.com/suprelory/octopus-plus/polywire/inbound/anthropic"
	"github.com/suprelory/octopus-plus/polywire/inbound/openai"
	"github.com/suprelory/octopus-plus/polywire/model"
)

type InboundType int

const (
	InboundTypeOpenAIChat InboundType = iota
	InboundTypeOpenAIResponse
	InboundTypeAnthropic
	InboundTypeOpenAIEmbedding
)

var inboundFactories = map[InboundType]func(config.Config) model.Inbound{
	InboundTypeOpenAIChat:      func(config.Config) model.Inbound { return &openai.ChatInbound{} },
	InboundTypeOpenAIResponse:  func(config.Config) model.Inbound { return &openai.ResponseInbound{} },
	InboundTypeOpenAIEmbedding: func(config.Config) model.Inbound { return &openai.EmbeddingInbound{} },
	InboundTypeAnthropic:       func(cfg config.Config) model.Inbound { return anthropic.New(cfg) },
}

// Get returns a fresh adapter with host services disabled. Use a Polywire Engine
// or New to inject diagnostics, token estimation and shared signature storage.
func Get(inboundType InboundType) model.Inbound {
	return New(inboundType, config.Config{})
}

func New(inboundType InboundType, cfg config.Config) model.Inbound {
	if factory, ok := inboundFactories[inboundType]; ok {
		return factory(cfg)
	}
	return nil
}

func Types() []InboundType {
	types := make([]InboundType, 0, len(inboundFactories))
	for typ := range inboundFactories {
		types = append(types, typ)
	}
	sort.Slice(types, func(i, j int) bool { return types[i] < types[j] })
	return types
}
