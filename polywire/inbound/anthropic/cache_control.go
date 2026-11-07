package anthropic

import (
	"github.com/suprelory/octopus-plus/polywire/model"
)

func convertToLLMCacheControl(c *CacheControl) *model.CacheControl {
	if c == nil {
		return nil
	}

	return &model.CacheControl{
		Type: c.Type,
		TTL:  c.TTL,
	}
}
