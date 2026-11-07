package gemini

import (
	"context"
	"net/http"

	"github.com/suprelory/octopus-plus/polywire/model"
)

func (o *MessagesOutbound) TransformError(_ context.Context, statusCode int, headers http.Header, body []byte) *model.ResponseError {
	return model.NormalizeHTTPError(statusCode, headers, body, "upstream_error")
}
