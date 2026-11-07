package outbound

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/suprelory/octopus-plus/polywire/model"
)

// BuildRequest returns the wire request and the same conversion evidence used
// by capability planning. It performs no network I/O and never consumes Body.
func BuildRequest(ctx context.Context, adapter model.Outbound, typ OutboundType, req *model.InternalLLMRequest, baseURL, key string) (*http.Request, LossReport, error) {
	if req == nil || adapter == nil {
		return nil, nil, fmt.Errorf("request and adapter are required")
	}
	descriptor, ok := Descriptor(typ)
	if !ok {
		return nil, nil, fmt.Errorf("unsupported outbound type %d", typ)
	}
	wire, err := adapter.TransformRequest(ctx, req, baseURL, key)
	if err != nil {
		return nil, nil, err
	}
	if wire.GetBody == nil {
		wire.Body.Close()
		return nil, nil, fmt.Errorf("outbound request does not expose a replayable body for conversion reporting")
	}
	reader, err := wire.GetBody()
	if err != nil {
		wire.Body.Close()
		return nil, nil, err
	}
	body, err := io.ReadAll(reader)
	reader.Close()
	if err != nil {
		wire.Body.Close()
		return nil, nil, err
	}
	report, err := describeWireConversion(req, adapter, descriptor, body)
	if err != nil {
		wire.Body.Close()
		return nil, nil, err
	}
	return wire, report, nil
}
