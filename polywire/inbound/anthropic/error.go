package anthropic

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/suprelory/octopus-plus/polywire/model"
)

func (i *MessagesInbound) TransformError(_ context.Context, response *model.ResponseError) (*model.ProtocolErrorResponse, error) {
	normalized := model.NormalizeResponseError(response, http.StatusBadGateway, "api_error")
	body, err := json.Marshal(struct {
		Type      string      `json:"type"`
		Error     errorDetail `json:"error"`
		RequestID string      `json:"request_id,omitempty"`
	}{
		Type: "error",
		Error: errorDetail{
			Type:    normalized.Detail.Type,
			Message: normalized.Detail.Message,
		},
		RequestID: normalized.Detail.RequestID,
	})
	if err != nil {
		return nil, err
	}
	return &model.ProtocolErrorResponse{
		StatusCode: normalized.StatusCode,
		Headers:    http.Header{"Content-Type": []string{"application/json"}},
		Body:       body,
		StreamBody: append(append([]byte("event: error\ndata: "), body...), []byte("\n\n")...),
	}, nil
}

type errorDetail struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}
