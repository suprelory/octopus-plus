package helper

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/dlclark/regexp2"
	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/polywire/outbound"
)

const modelFetchUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/132.0.0.0 Safari/537.36"

// modelResponseBodyLimit caps how much of a channel's model listing is buffered.
const modelResponseBodyLimit = 32 << 20

// modelFetchRequestTimeout bounds one upstream model-listing request. The shared
// relay client has no Timeout (streaming relays need none) and callers only pass
// a batch-wide deadline, so without a per-request bound a single unresponsive
// upstream stalls the whole batch — site sync fetches models once per candidate
// base URL in a loop. Paginated listings get this bound per page, not per call.
const modelFetchRequestTimeout = 60 * time.Second

// modelFetchOperationTimeout bounds the complete model-listing operation,
// including every page and any protocol fallback. Per-request timeouts alone do
// not stop a broken upstream from returning an endless sequence of pages.
const modelFetchOperationTimeout = 5 * time.Minute

// modelFetchMaxPages limits retained pagination state and protects callers from
// upstreams that keep advertising new cursors indefinitely.
const modelFetchMaxPages = 100

func FetchModels(ctx context.Context, request model.Channel) ([]string, error) {
	return fetchModels(ctx, request, modelFetchRequestTimeout, modelFetchOperationTimeout)
}

// fetchModels takes both bounds explicitly so tests can shorten them without
// mutating package state.
func fetchModels(ctx context.Context, request model.Channel, requestTimeout, operationTimeout time.Duration) ([]string, error) {
	if _, ok := outbound.Descriptor(request.Type); !ok {
		return nil, fmt.Errorf("unsupported channel type: %d", request.Type)
	}
	operationCtx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()

	client, err := ChannelHTTPClientWithContext(operationCtx, &request)
	if err != nil {
		return nil, err
	}
	var fetchModel []string
	switch request.Type {
	case outbound.OutboundTypeAnthropic:
		fetchModel, err = fetchAnthropicModels(client, operationCtx, request, requestTimeout)
	case outbound.OutboundTypeGemini:
		fetchModel, err = fetchGeminiModels(client, operationCtx, request, requestTimeout)
	default:
		fetchModel, err = fetchOpenAIModels(client, operationCtx, request, requestTimeout)
	}
	if err != nil {
		return nil, err
	}
	if request.MatchRegex != nil && *request.MatchRegex != "" {
		matchModel := make([]string, 0)
		re, err := regexp2.Compile(*request.MatchRegex, regexp2.ECMAScript)
		if err != nil {
			return nil, err
		}
		for _, model := range fetchModel {
			matched, err := re.MatchString(model)
			if err != nil {
				return nil, err
			}
			if matched {
				matchModel = append(matchModel, model)
			}
		}
		return matchModel, nil
	}
	return fetchModel, nil
}

// doBoundedModelRequest issues one model-listing request under its own deadline
// and decodes the response into result. The deadline covers building, sending and
// reading a single request, so paginated callers can invoke this per page without
// letting hung pages accumulate.
func doBoundedModelRequest(client *http.Client, ctx context.Context, requestTimeout time.Duration, buildRequest func(context.Context) (*http.Request, error), result any) error {
	reqCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	req, err := buildRequest(reqCtx)
	if err != nil {
		return err
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return decodeModelJSONResponse(resp, result)
}

// refer: https://platform.openai.com/docs/api-reference/models/list
func fetchOpenAIModels(client *http.Client, ctx context.Context, request model.Channel, requestTimeout time.Duration) ([]string, error) {
	var result model.OpenAIModelList
	err := doBoundedModelRequest(client, ctx, requestTimeout, func(reqCtx context.Context) (*http.Request, error) {
		req, err := http.NewRequestWithContext(
			reqCtx,
			http.MethodGet,
			request.GetBaseUrl()+"/models",
			nil,
		)
		if err != nil {
			return nil, err
		}
		applyDefaultModelRequestHeaders(req, request)
		req.Header.Set("Authorization", "Bearer "+request.GetChannelKey().ChannelKey)
		return req, nil
	}, &result)
	if err != nil {
		return nil, err
	}

	models := make([]string, 0, len(result.Data))
	for _, m := range result.Data {
		models = append(models, m.ID)
	}
	return models, nil
}

// refer: https://ai.google.dev/api/models
func fetchGeminiModels(client *http.Client, ctx context.Context, request model.Channel, requestTimeout time.Duration) ([]string, error) {
	var allModels []string
	pageToken := ""
	seenPageTokens := map[string]struct{}{pageToken: {}}

	for page := 1; page <= modelFetchMaxPages; page++ {
		currentPageToken := pageToken
		var result model.GeminiModelList
		err := doBoundedModelRequest(client, ctx, requestTimeout, func(reqCtx context.Context) (*http.Request, error) {
			req, err := http.NewRequestWithContext(
				reqCtx,
				http.MethodGet,
				request.GetBaseUrl()+"/models",
				nil,
			)
			if err != nil {
				return nil, err
			}
			applyDefaultModelRequestHeaders(req, request)
			req.Header.Set("X-Goog-Api-Key", request.GetChannelKey().ChannelKey)
			if currentPageToken != "" {
				q := req.URL.Query()
				q.Add("pageToken", currentPageToken)
				req.URL.RawQuery = q.Encode()
			}
			return req, nil
		}, &result)
		if err != nil {
			return nil, err
		}

		for _, m := range result.Models {
			name := strings.TrimPrefix(m.Name, "models/")
			allModels = append(allModels, name)
		}

		if result.NextPageToken == "" {
			break
		}
		if _, seen := seenPageTokens[result.NextPageToken]; seen {
			return nil, fmt.Errorf("gemini model pagination returned a repeated page token after page %d", page)
		}
		if page == modelFetchMaxPages {
			return nil, fmt.Errorf("gemini model pagination exceeded the %d-page limit", modelFetchMaxPages)
		}
		seenPageTokens[result.NextPageToken] = struct{}{}
		pageToken = result.NextPageToken
	}
	if len(allModels) == 0 {
		return fetchOpenAIModels(client, ctx, request, requestTimeout)
	}
	return allModels, nil
}

// refer: https://platform.claude.com/docs
func fetchAnthropicModels(client *http.Client, ctx context.Context, request model.Channel, requestTimeout time.Duration) ([]string, error) {
	var allModels []string
	var afterID string
	seenAfterIDs := map[string]struct{}{afterID: {}}
	for page := 1; page <= modelFetchMaxPages; page++ {
		currentAfterID := afterID
		var result model.AnthropicModelList
		err := doBoundedModelRequest(client, ctx, requestTimeout, func(reqCtx context.Context) (*http.Request, error) {
			req, err := http.NewRequestWithContext(
				reqCtx,
				http.MethodGet,
				request.GetBaseUrl()+"/models",
				nil,
			)
			if err != nil {
				return nil, err
			}
			applyDefaultModelRequestHeaders(req, request)
			req.Header.Set("X-Api-Key", request.GetChannelKey().ChannelKey)
			req.Header.Set("Anthropic-Version", "2023-06-01")
			q := req.URL.Query()
			if currentAfterID != "" {
				q.Set("after_id", currentAfterID)
			}
			req.URL.RawQuery = q.Encode()
			return req, nil
		}, &result)
		if err != nil {
			return nil, err
		}

		for _, m := range result.Data {
			allModels = append(allModels, m.ID)
		}

		if !result.HasMore {
			break
		}
		if result.LastID == "" {
			return nil, fmt.Errorf("anthropic model pagination omitted last_id after page %d", page)
		}
		if _, seen := seenAfterIDs[result.LastID]; seen {
			return nil, fmt.Errorf("anthropic model pagination returned a repeated last_id after page %d", page)
		}
		if page == modelFetchMaxPages {
			return nil, fmt.Errorf("anthropic model pagination exceeded the %d-page limit", modelFetchMaxPages)
		}
		seenAfterIDs[result.LastID] = struct{}{}
		afterID = result.LastID
	}
	if len(allModels) == 0 {
		return fetchOpenAIModels(client, ctx, request, requestTimeout)
	}
	return allModels, nil
}

func applyDefaultModelRequestHeaders(req *http.Request, request model.Channel) {
	if req == nil {
		return
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", modelFetchUserAgent)
	}
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json, text/plain, */*")
	}
	if req.Header.Get("Accept-Language") == "" {
		req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	}
	for _, header := range request.CustomHeader {
		if header.HeaderKey != "" {
			req.Header.Set(header.HeaderKey, header.HeaderValue)
		}
	}
}

func decodeModelJSONResponse(resp *http.Response, result any) error {
	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, modelResponseBodyLimit+1))
	if err != nil {
		return err
	}
	if len(bodyBytes) > modelResponseBodyLimit {
		return fmt.Errorf("upstream model response exceeds the %d MiB limit", modelResponseBodyLimit>>20)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return formatModelHTTPError(resp.StatusCode, resp.Header.Get("Content-Type"), bodyBytes)
	}
	if len(bodyBytes) == 0 {
		return nil
	}
	if err := json.Unmarshal(bodyBytes, result); err != nil {
		if summary := extractModelHTMLResponseSummary(resp.Header.Get("Content-Type"), bodyBytes); summary != "" {
			return fmt.Errorf("decode response failed: %s", summary)
		}
		return fmt.Errorf("decode response failed: %w", err)
	}
	return nil
}

func formatModelHTTPError(statusCode int, contentType string, bodyBytes []byte) error {
	if payload, ok := parseModelErrorPayload(bodyBytes); ok {
		if message := extractModelErrorMessage(payload); message != "" {
			return fmt.Errorf("http %d: %s", statusCode, message)
		}
	}
	if summary := extractModelHTMLResponseSummary(contentType, bodyBytes); summary != "" {
		return fmt.Errorf("http %d: %s", statusCode, summary)
	}
	message := strings.TrimSpace(string(bodyBytes))
	if message == "" || IsHTMLResponse(contentType, message) {
		message = http.StatusText(statusCode)
	}
	if message == "" {
		return fmt.Errorf("http %d", statusCode)
	}
	return fmt.Errorf("http %d: %s", statusCode, message)
}

func parseModelErrorPayload(bodyBytes []byte) (map[string]any, bool) {
	if len(bodyBytes) == 0 {
		return map[string]any{}, true
	}
	var payload map[string]any
	if err := json.Unmarshal(bodyBytes, &payload); err != nil {
		return nil, false
	}
	return payload, true
}

func extractModelErrorMessage(payload map[string]any) string {
	if payload == nil {
		return ""
	}
	if message, ok := payload["message"].(string); ok && strings.TrimSpace(message) != "" {
		return strings.TrimSpace(message)
	}
	if message, ok := payload["msg"].(string); ok && strings.TrimSpace(message) != "" {
		return strings.TrimSpace(message)
	}
	if errorPayload, ok := payload["error"].(map[string]any); ok {
		if message, ok := errorPayload["message"].(string); ok && strings.TrimSpace(message) != "" {
			return strings.TrimSpace(message)
		}
	}
	return ""
}

func extractModelHTMLResponseSummary(contentType string, bodyBytes []byte) string {
	body := strings.TrimSpace(string(bodyBytes))
	if body == "" {
		return ""
	}
	lowered := strings.ToLower(body)
	if !IsHTMLResponse(contentType, body) {
		if cloudflareWaitingTitlePattern.MatchString(body) {
			return "Just a moment..."
		}
		return ""
	}
	if IsCloudflareProtectionMessage(body) {
		if cloudflareWaitingTitlePattern.MatchString(ExtractHTMLTitle(body)) {
			return "Just a moment..."
		}
		return "Cloudflare challenge"
	}
	if title := ExtractHTMLTitle(body); title != "" {
		return title
	}
	if strings.Contains(lowered, "cloudflare tunnel error") {
		return "Cloudflare Tunnel error"
	}
	return ""
}
