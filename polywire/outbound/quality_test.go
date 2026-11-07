package outbound

import (
	"testing"

	"github.com/suprelory/octopus-plus/polywire/model"
)

func TestStaticQualityMatrixCoversEveryCombination(t *testing.T) {
	want := len(matrixInboundFormats) * len(protocolDescriptors) * len(matrixRequestTypes)
	matrix := StaticQualityMatrix()
	if len(matrix) != want {
		t.Fatalf("matrix entries = %d, want %d", len(matrix), want)
	}
	if got := StaticConversionQuality(model.APIFormatAnthropicMessage, OutboundTypeAnthropic, model.RequestTypeChat); got != QualityNative {
		t.Fatalf("Anthropic passthrough quality = %q", got)
	}
	if got := StaticConversionQuality(model.APIFormatOpenAIEmbedding, OutboundTypeOpenAIEmbedding, model.RequestTypeEmbedding); got != QualityLossless {
		t.Fatalf("embedding quality = %q", got)
	}
	if got := StaticConversionQuality(model.APIFormatOpenAIResponse, OutboundTypeGemini, model.RequestTypeResponses); got != QualityConditional {
		t.Fatalf("Responses to Gemini quality = %q", got)
	}
	if got := StaticConversionQuality(model.APIFormatOpenAIChatCompletion, OutboundTypeOpenAIEmbedding, model.RequestTypeChat); got != QualityUnsupported {
		t.Fatalf("chat to embedding quality = %q", got)
	}
	if got := StaticConversionQuality(model.APIFormatOpenAIImageGeneration, OutboundTypeOpenAIResponse, model.RequestTypeResponses); got != QualityUnsupported {
		t.Fatalf("mismatched format/operation quality = %q", got)
	}
}
