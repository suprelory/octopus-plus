package outbound

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"github.com/suprelory/octopus-plus/polywire/model"
)

// PreparedRequest owns a validated body and its conversion report. It is
// immutable and contains no channel credentials or caller context. Build creates
// an independent HTTP envelope; response adapters must still be fresh per attempt.
type PreparedRequest struct {
	template *http.Request
	target   model.RequestTarget
	targeter model.RequestTargeter
	report   LossReport
}

func prepareWireRequest(wire *http.Request, adapter model.Outbound, request *model.InternalLLMRequest, report LossReport) *PreparedRequest {
	targeter, ok := adapter.(model.RequestTargeter)
	if !ok || wire.GetBody == nil {
		return nil
	}
	target := model.RequestTargetFrom(request)
	if target.Query != nil {
		query := make(url.Values, len(target.Query))
		for name, values := range target.Query {
			query[name] = append([]string(nil), values...)
		}
		target.Query = query
	}
	template := wire.Clone(context.Background())
	template.Body = http.NoBody
	return &PreparedRequest{template: template, target: target, targeter: targeter, report: append(LossReport(nil), report...)}
}

// Size returns the retained wire body size, for request-scoped cache budgets.
func (p *PreparedRequest) Size() int64 {
	if p == nil || p.template == nil {
		return 0
	}
	return p.template.ContentLength
}

func (p *PreparedRequest) Build(ctx context.Context, baseURL, key string) (*http.Request, LossReport, error) {
	if p == nil || p.template == nil || p.targeter == nil || ctx == nil {
		return nil, nil, fmt.Errorf("prepared request and context are required")
	}
	wire := p.template.Clone(ctx)
	reader, err := p.template.GetBody()
	if err != nil {
		return nil, nil, err
	}
	wire.Body = reader
	if err := p.targeter.RetargetRequest(wire, p.target, baseURL, key); err != nil {
		wire.Body.Close()
		return nil, nil, err
	}
	return wire, append(LossReport(nil), p.report...), nil
}
