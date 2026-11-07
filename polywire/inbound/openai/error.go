package openai

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/suprelory/octopus-plus/polywire/model"
)

func transformOpenAIError(response *model.ResponseError) (*model.ProtocolErrorResponse, error) {
	normalized := model.NormalizeResponseError(response, http.StatusBadGateway, "api_error")
	body, err := json.Marshal(struct {
		Error model.ErrorDetail `json:"error"`
	}{Error: normalized.Detail})
	if err != nil {
		return nil, err
	}
	return &model.ProtocolErrorResponse{
		StatusCode: normalized.StatusCode,
		Headers:    http.Header{"Content-Type": []string{"application/json"}},
		Body:       body,
		StreamBody: append(append([]byte("data: "), body...), []byte("\n\n")...),
	}, nil
}

func (i *ChatInbound) TransformError(_ context.Context, response *model.ResponseError) (*model.ProtocolErrorResponse, error) {
	return transformOpenAIError(response)
}

func (i *EmbeddingInbound) TransformError(_ context.Context, response *model.ResponseError) (*model.ProtocolErrorResponse, error) {
	return transformOpenAIError(response)
}

func (i *ResponseInbound) TransformError(_ context.Context, response *model.ResponseError) (*model.ProtocolErrorResponse, error) {
	transformed, err := transformOpenAIError(response)
	if err != nil {
		return nil, err
	}
	normalized := model.NormalizeResponseError(response, http.StatusBadGateway, "api_error")
	streamBody, err := json.Marshal(struct {
		Type    string `json:"type"`
		Code    string `json:"code,omitempty"`
		Message string `json:"message"`
		Param   string `json:"param,omitempty"`
	}{
		Type:    "error",
		Code:    normalized.Detail.Code,
		Message: normalized.Detail.Message,
		Param:   normalized.Detail.Param,
	})
	if err != nil {
		return nil, err
	}
	transformed.StreamBody = append(append([]byte("event: error\ndata: "), streamBody...), []byte("\n\n")...)
	return transformed, nil
}
