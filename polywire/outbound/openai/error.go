package openai

import (
	"context"
	"net/http"

	"github.com/suprelory/octopus-plus/polywire/model"
)

func transformOpenAIError(statusCode int, headers http.Header, body []byte) *model.ResponseError {
	return model.NormalizeHTTPError(statusCode, headers, body, "api_error")
}

func (o *ChatOutbound) TransformError(_ context.Context, statusCode int, headers http.Header, body []byte) *model.ResponseError {
	return transformOpenAIError(statusCode, headers, body)
}

func (o *ResponseOutbound) TransformError(_ context.Context, statusCode int, headers http.Header, body []byte) *model.ResponseError {
	return transformOpenAIError(statusCode, headers, body)
}

func (o *EmbeddingOutbound) TransformError(_ context.Context, statusCode int, headers http.Header, body []byte) *model.ResponseError {
	return transformOpenAIError(statusCode, headers, body)
}
