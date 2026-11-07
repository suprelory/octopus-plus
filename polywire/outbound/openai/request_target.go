package openai

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/suprelory/octopus-plus/polywire/model"
)

func retargetOpenAIRequest(wire *http.Request, baseURL, key, path string) error {
	endpoint, err := url.Parse(strings.TrimSuffix(baseURL, "/"))
	if err != nil {
		return fmt.Errorf("failed to parse base url: %w", err)
	}
	endpoint.Path += path
	wire.URL = endpoint
	wire.Header.Set("Authorization", "Bearer "+key)
	return nil
}

func (*ChatOutbound) RetargetRequest(wire *http.Request, _ model.RequestTarget, baseURL, key string) error {
	return retargetOpenAIRequest(wire, baseURL, key, "/chat/completions")
}

func (*ResponseOutbound) RetargetRequest(wire *http.Request, _ model.RequestTarget, baseURL, key string) error {
	return retargetOpenAIRequest(wire, baseURL, key, "/responses")
}

func (*EmbeddingOutbound) RetargetRequest(wire *http.Request, _ model.RequestTarget, baseURL, key string) error {
	return retargetOpenAIRequest(wire, baseURL, key, "/embeddings")
}
