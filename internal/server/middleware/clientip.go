package middleware

import (
	"fmt"
	"sync/atomic"

	"github.com/gin-gonic/gin"
	clientipresolver "github.com/suprelory/octopus-plus/internal/clientip"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/utils/log"
)

var activeClientIPResolver atomic.Pointer[clientipresolver.Resolver]

func init() {
	// Keep a no-trust resolver available before the setting cache is loaded.
	_ = ConfigureTrustedProxies("")
	op.RegisterSettingApplier(model.SettingKeyTrustedProxies, applyTrustedProxies)
}

func applyTrustedProxies(raw string) error {
	if err := ConfigureTrustedProxies(raw); err != nil {
		// A malformed restored value must never retain the previous trust set.
		_ = ConfigureTrustedProxies("")
		return err
	}
	return nil
}

// ConfigureTrustedProxies atomically replaces the resolver used by new
// requests. Existing requests keep using the immutable resolver they loaded.
func ConfigureTrustedProxies(raw string) error {
	resolver, _, err := clientipresolver.ParseTrustedProxies(raw)
	if err != nil {
		return err
	}
	activeClientIPResolver.Store(resolver)
	if resolver.TrustsAll() {
		log.Warnf("trusted proxy setting includes a catch-all CIDR; clients may be able to spoof forwarded IP headers if the service is directly reachable")
	}
	return nil
}

// ReloadTrustedProxies applies the database-backed runtime setting.
func ReloadTrustedProxies() error {
	raw, err := op.SettingGetString(model.SettingKeyTrustedProxies)
	if err != nil {
		_ = ConfigureTrustedProxies("")
		return fmt.Errorf("load trusted proxies setting: %w", err)
	}
	if err := applyTrustedProxies(raw); err != nil {
		return fmt.Errorf("configure trusted proxies: %w", err)
	}
	return nil
}

// TrustedProxyCount returns the number of active IP/CIDR entries.
func TrustedProxyCount() int {
	resolver := activeClientIPResolver.Load()
	if resolver == nil {
		return 0
	}
	return resolver.Count()
}

// ClientIP returns the direct peer unless that peer is trusted, in which case
// it resolves X-Forwarded-For/X-Real-IP from right to left.
func ClientIP(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	resolver := activeClientIPResolver.Load()
	if resolver == nil {
		return c.Request.RemoteAddr
	}
	return resolver.Resolve(c.Request.RemoteAddr, c.Request.Header)
}
