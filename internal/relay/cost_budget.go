package relay

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"

	"github.com/suprelory/octopus-plus/internal/apperror"
	dbmodel "github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/polywire/model"
	"github.com/suprelory/octopus-plus/polywire/outbound"
)

type costBudgetError struct{ response *model.ResponseError }

func (e *costBudgetError) Error() string { return e.response.Detail.Message }
func (e *costBudgetError) Unwrap() error { return e.response }

func newCostBudgetError(message string) error {
	return &costBudgetError{relayProtocolError(http.StatusBadRequest, apperror.CodeAuthAPIKeyCostExceeded, message)}
}

func isCostBudgetError(err error) bool {
	var budgetErr *costBudgetError
	return errors.As(err, &budgetErr)
}

// Validate the actual bytes after protocol conversion and channel overrides.
// The configured client-model price is also frozen for final settlement.
func (ra *relayAttempt) checkRequestCost(payload []byte) error {
	reservation := op.APIKeyCostReservationFromContext(ra.requestContext())
	if !reservation.Limited() {
		return nil
	}
	if ra.metrics.costPrice == nil {
		price := resolveModelPrice(ra.requestModel)
		if price == nil {
			return newCostBudgetError("cost-limited API keys require a configured model price")
		}
		copy := *price
		ra.metrics.costPrice = &copy
	}
	cost, err := maximumRequestCost(ra.channel.Type, payload, *ra.metrics.costPrice)
	if err != nil {
		return newCostBudgetError(err.Error())
	}
	ra.estimatedCost, ra.costChecked = cost, true
	return nil
}

func maximumRequestCost(protocol outbound.OutboundType, payload []byte, price dbmodel.LLMPrice) (float64, error) {
	for _, rate := range []float64{price.Input, price.Output, price.CacheRead, price.CacheWrite} {
		if rate < 0 || math.IsNaN(rate) || math.IsInf(rate, 0) {
			return 0, fmt.Errorf("cost-limited API keys require finite, non-negative model prices")
		}
	}
	inputRate := math.Max(price.Input, math.Max(price.CacheRead, price.CacheWrite))
	if inputRate == 0 && price.Output == 0 {
		return 0, fmt.Errorf("cost-limited API keys require a non-zero model price")
	}
	var body map[string]any
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := decoder.Decode(&body); err != nil || body == nil || !json.Valid(payload) {
		return 0, fmt.Errorf("cannot estimate cost for a non-JSON request")
	}
	if hasUnmeteredCostInput(body) {
		return 0, fmt.Errorf("cost-limited API keys require text input without remote media, hosted tools or opaque continuation state")
	}
	var maxOutput int64
	candidates := int64(1)
	var err error
	switch protocol {
	case outbound.OutboundTypeOpenAIChat:
		// Budget for the larger limit when both legacy and current fields exist.
		for _, field := range []string{"max_tokens", "max_completion_tokens"} {
			if value, exists := body[field]; exists {
				limit, parseErr := positiveCostInteger(value)
				if parseErr != nil {
					return 0, fmt.Errorf("%s must be a positive integer for cost-limited API keys", field)
				}
				maxOutput = max(maxOutput, limit)
			}
		}
		if value, exists := body["n"]; exists {
			candidates, err = positiveCostInteger(value)
		}
	case outbound.OutboundTypeOpenAIResponse:
		maxOutput, err = positiveCostInteger(body["max_output_tokens"])
	case outbound.OutboundTypeAnthropic:
		maxOutput, err = positiveCostInteger(body["max_tokens"])
	case outbound.OutboundTypeGemini:
		config, _ := body["generationConfig"].(map[string]any)
		maxOutput, err = positiveCostInteger(config["maxOutputTokens"])
		if value, exists := config["candidateCount"]; exists && err == nil {
			candidates, err = positiveCostInteger(value)
		}
	case outbound.OutboundTypeOpenAIEmbedding:
		// Embeddings have no generated output-token charge.
	default:
		return 0, fmt.Errorf("this protocol does not support cost-limited requests")
	}
	if err != nil || (protocol != outbound.OutboundTypeOpenAIEmbedding && maxOutput <= 0) {
		return 0, fmt.Errorf("cost-limited API keys require a positive output-token limit and candidate count")
	}
	// UTF-8 bytes plus framing allowance deliberately overestimate ordinary
	// text token counts. Provider tokenization/usage remains the billing source;
	// this is a local admission estimate, not a supplier invoice guarantee.
	inputTokens := float64(len(payload)) + 1024
	cost := (inputTokens*inputRate + float64(maxOutput)*float64(candidates)*price.Output) * 1e-6
	if math.IsInf(cost, 0) || math.IsNaN(cost) {
		return 0, fmt.Errorf("request cost estimate is not finite")
	}
	return math.Nextafter(cost, math.Inf(1)), nil
}

func positiveCostInteger(value any) (int64, error) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, fmt.Errorf("not an integer")
	}
	n, err := number.Int64()
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("not a positive integer")
	}
	return n, nil
}

func hasUnmeteredCostInput(value any) bool {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			if child == nil || child == "" {
				continue
			}
			switch strings.ToLower(key) {
			case "previous_response_id", "conversation", "cachedcontent", "cached_content",
				"image_url", "file_id", "file_data", "input_audio", "audio", "inlinedata", "filedata", "inline_data", "encrypted_content":
				return true
			case "type":
				if typ, ok := child.(string); ok {
					switch typ {
					case "image", "input_image", "input_file", "document", "audio", "input_audio", "video":
						return true
					}
				}
			case "modalities", "responsemodalities":
				items, ok := child.([]any)
				if !ok {
					return true
				}
				for _, item := range items {
					if item != "text" && item != "TEXT" {
						return true
					}
				}
			case "tools":
				items, ok := child.([]any)
				if !ok {
					return true
				}
				for _, item := range items {
					tool, ok := item.(map[string]any)
					if !ok {
						return true
					}
					if typ, exists := tool["type"]; exists && typ != "function" {
						return true
					}
					_, named := tool["name"]
					_, function := tool["function"]
					_, declarations := tool["functionDeclarations"]
					if !named && !function && !declarations {
						return true
					}
				}
			case "service_tier":
				if child != "default" {
					return true
				}
			}
			if hasUnmeteredCostInput(child) {
				return true
			}
		}
	case []any:
		for _, child := range v {
			if hasUnmeteredCostInput(child) {
				return true
			}
		}
	}
	return false
}
