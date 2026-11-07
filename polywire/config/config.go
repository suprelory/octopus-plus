// Package config defines the optional host services used by protocol adapters.
package config

import "github.com/suprelory/octopus-plus/polywire/compat"

// Logger receives conversion diagnostics. Implementations must support concurrent
// calls when shared by an Engine. Polywire never configures a process-wide logger.
type Logger interface {
	Warnf(format string, args ...any)
	Warnw(message string, keysAndValues ...any)
	Debugw(message string, keysAndValues ...any)
}

// TokenCounter estimates input tokens for Anthropic's initial streaming usage.
// It must support concurrent calls when shared by an Engine.
type TokenCounter func(content, model string) int

// Config contains host services, copied into each newly created adapter.
// A zero Config disables diagnostics, input-token estimation and signature storage.
// polywire.New supplies an instance-owned memory store when SignatureStore is nil.
type Config struct {
	Logger         Logger
	TokenCounter   TokenCounter
	SignatureStore compat.SignatureStore
}

// Log returns the configured logger or a silent logger.
func (c Config) Log() Logger {
	if c.Logger != nil {
		return c.Logger
	}
	return discardLogger{}
}

// CountTokens returns zero when estimation is unavailable. Actual provider usage
// remains authoritative; callers can inject their existing tokenizer unchanged.
func (c Config) CountTokens(content, model string) int {
	if c.TokenCounter == nil {
		return 0
	}
	return c.TokenCounter(content, model)
}

type discardLogger struct{}

func (discardLogger) Warnf(string, ...any)  {}
func (discardLogger) Warnw(string, ...any)  {}
func (discardLogger) Debugw(string, ...any) {}
