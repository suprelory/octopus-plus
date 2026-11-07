package outbound

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/suprelory/octopus-plus/polywire/config"
	"github.com/suprelory/octopus-plus/polywire/model"
)

func TestPreparedRequestMatchesFreshBuildAndIsolatesAttempts(t *testing.T) {
	for _, typ := range []OutboundType{OutboundTypeOpenAIChat, OutboundTypeOpenAIResponse, OutboundTypeAnthropic, OutboundTypeGemini, OutboundTypeOpenAIEmbedding} {
		t.Run(typ.String(), func(t *testing.T) {
			text, streaming := "hello", true
			request := &model.InternalLLMRequest{
				Model: "client", Stream: &streaming, RawAPIFormat: model.APIFormatOpenAIChatCompletion,
				Messages:            []model.Message{{Role: "user", Content: model.MessageContent{Content: &text}}},
				Query:               url.Values{"trace": {"original"}},
				TransformerMetadata: map[string]string{model.TransformerMetadataOpenAIOrganization: "org-test"},
			}
			if typ == OutboundTypeOpenAIEmbedding {
				request.Messages, request.Stream = nil, nil
				request.EmbeddingInput = &model.EmbeddingInput{Single: &text}
				request.RequestType = model.RequestTypeEmbedding
				request.RawAPIFormat = model.APIFormatOpenAIEmbedding
			}
			decision, prepared := PrepareRequestForModelWithConfig(request, "upstream", typ, false, config.Config{})
			if decision.Rejected() || prepared == nil {
				t.Fatalf("failed to prepare: %+v", decision)
			}
			original := request.Clone()
			original.Model = "upstream"
			// The prepared body and transport inputs must be a snapshot.
			text, streaming = "changed", false
			request.Query.Set("trace", "changed")
			request.TransformerMetadata[model.TransformerMetadataOpenAIOrganization] = "changed"
			for index, baseURL := range []string{"https://one.example/v1?region=west", "https://two.example/proxy?region=east"} {
				key := fmt.Sprintf("test-key-%d", index)
				ctx, cancel := context.WithCancel(context.Background())
				wire, report, err := prepared.Build(ctx, baseURL, key)
				if err != nil {
					cancel()
					t.Fatal(err)
				}
				fresh, freshReport, err := BuildRequest(ctx, Get(typ), typ, original, baseURL, key)
				if err != nil {
					cancel()
					t.Fatal(err)
				}
				body, err := io.ReadAll(wire.Body)
				if err != nil {
					t.Fatal(err)
				}
				freshBody, err := io.ReadAll(fresh.Body)
				if err != nil {
					t.Fatal(err)
				}
				wire.Body.Close()
				fresh.Body.Close()
				if !bytes.Equal(body, freshBody) || wire.URL.String() != fresh.URL.String() || !reflect.DeepEqual(wire.Header, fresh.Header) ||
					!reflect.DeepEqual(report, freshReport) || !reflect.DeepEqual(report, decision.ConversionReport) || prepared.Size() != int64(len(body)) {
					t.Fatalf("prepared request differs from fresh build: prepared=%s %v %s; fresh=%s %v %s", wire.URL, wire.Header, body, fresh.URL, fresh.Header, freshBody)
				}
				if wire.Context() != ctx || wire.URL.Host == "conversion.invalid" {
					t.Fatal("attempt retained planning context or endpoint")
				}
				replay, err := wire.GetBody()
				if err != nil {
					t.Fatal(err)
				}
				replayed, err := io.ReadAll(replay)
				replay.Close()
				if err != nil || !bytes.Equal(body, replayed) {
					t.Fatal("request body is not replayable")
				}
				wire.Header.Set("Authorization", "modified")
				wire.URL.Path = "/modified"
				if len(report) > 0 {
					report[0].Reason = "modified"
				}
				cancel()
			}
			if wire, _, err := prepared.Build(context.Background(), "http://[::1", "test"); err == nil || wire != nil {
				t.Fatal("invalid endpoint was accepted")
			}
		})
	}
}

func TestPreparedRequestSkippedForPassthroughAndRejection(t *testing.T) {
	request := &model.InternalLLMRequest{Model: "client", RequestType: model.RequestTypeChat, RawAPIFormat: model.APIFormatAnthropicMessage}
	decision, prepared := PrepareRequestForModelWithConfig(request, "upstream", OutboundTypeAnthropic, true, config.Config{})
	if !decision.Passthrough || prepared != nil {
		t.Fatalf("passthrough was converted: %+v", decision)
	}
	n := int64(2)
	request.N = &n
	decision, prepared = PrepareRequestForModelWithConfig(request, "upstream", OutboundTypeOpenAIChat, false, config.Config{})
	if !decision.Rejected() || prepared != nil {
		t.Fatalf("rejected request retained a body: %+v", decision)
	}
}

func BenchmarkPlanAndBuildRequest(b *testing.B) {
	for _, size := range []int{128, 1 << 20} {
		for _, reuse := range []bool{false, true} {
			b.Run(fmt.Sprintf("bytes=%d/reuse=%t", size, reuse), func(b *testing.B) {
				text := strings.Repeat("x", size)
				request := &model.InternalLLMRequest{Model: "claude-test", RawAPIFormat: model.APIFormatOpenAIChatCompletion,
					Messages: []model.Message{{Role: "user", Content: model.MessageContent{Content: &text}}}}
				b.ReportAllocs()
				b.SetBytes(int64(size))
				b.ResetTimer()
				for range b.N {
					var wire *http.Request
					var err error
					if reuse {
						decision, prepared := PrepareRequestForModelWithConfig(request, request.Model, OutboundTypeAnthropic, false, config.Config{})
						if decision.Rejected() || prepared == nil {
							b.Fatalf("prepare: %+v", decision)
						}
						wire, _, err = prepared.Build(context.Background(), "https://example.invalid/v1", "test")
					} else {
						decision := PlanRequestForModel(request, request.Model, OutboundTypeAnthropic, false)
						if decision.Rejected() {
							b.Fatalf("plan: %+v", decision)
						}
						wire, _, err = BuildRequest(context.Background(), Get(OutboundTypeAnthropic), OutboundTypeAnthropic, request, "https://example.invalid/v1", "test")
					}
					if err != nil {
						b.Fatal(err)
					}
					wire.Body.Close()
				}
			})
		}
	}
}
