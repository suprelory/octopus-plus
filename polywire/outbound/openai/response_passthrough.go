package openai

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/suprelory/octopus-plus/polywire/model"
	"github.com/suprelory/octopus-plus/polywire/rawjson"
)

// TransformRequestRaw keeps the original OpenAI Responses payload intact and only rewrites
// the top-level model to the selected upstream model.
func (o *ResponseOutbound) TransformRequestRaw(ctx context.Context, rawBody []byte, modelName, baseUrl, key string, query url.Values) (*http.Request, error) {
	if len(rawBody) == 0 {
		return nil, fmt.Errorf("raw body is empty")
	}
	if strings.TrimSpace(modelName) != "" {
		rewrittenBody, err := rewriteRawResponsesRequestModel(rawBody, modelName)
		if err != nil {
			return nil, err
		}
		rawBody = rewrittenBody
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "", bytes.NewReader(rawBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.ContentLength = int64(len(rawBody))
	bodyBytes := rawBody
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(bodyBytes)), nil
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	// OpenAI-Organization / OpenAI-Project are forwarded by the relay's
	// copyHeaders on the raw-passthrough path, so no explicit application
	// is needed here (O-M7).

	parsedURL, err := url.Parse(strings.TrimSuffix(baseUrl, "/"))
	if err != nil {
		return nil, fmt.Errorf("failed to parse base url: %w", err)
	}
	parsedURL.Path = parsedURL.Path + "/responses"
	if query != nil {
		parsedURL.RawQuery = query.Encode()
	}
	req.URL = parsedURL
	req.Method = http.MethodPost

	return req, nil
}

func rewriteRawResponsesRequestModel(rawBody []byte, modelName string) ([]byte, error) {
	return rawjson.ReplaceTopLevelString(rawBody, "model", strings.TrimSpace(modelName))
}

// CanPassthrough implements model.PassthroughCapable.
// Returns true when the inbound format is OpenAI Responses API.
func (o *ResponseOutbound) CanPassthrough(inboundFormat model.APIFormat) bool {
	return inboundFormat == model.APIFormatOpenAIResponse
}

// PassthroughConfig implements model.PassthroughCapable.
// Returns OpenAI Responses-specific passthrough settings.
func (o *ResponseOutbound) PassthroughConfig() model.PassthroughConfig {
	return model.PassthroughConfig{
		TerminalEvents: map[string]struct{}{
			"response.completed":  {},
			"response.failed":     {},
			"response.incomplete": {},
			"error":               {},
		},
		CollectMetrics: false, // OpenAI Responses uses different metrics semantics
	}
}
