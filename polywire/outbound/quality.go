package outbound

import (
	"sort"

	"github.com/suprelory/octopus-plus/polywire/model"
)

type ConversionQuality string

const (
	QualityNative      ConversionQuality = "native"
	QualityLossless    ConversionQuality = "lossless"
	QualityConditional ConversionQuality = "conditional"
	QualityUnsupported ConversionQuality = "unsupported"
)

type QualityMatrixEntry struct {
	InboundFormat  model.APIFormat
	OutboundType   OutboundType
	OutboundFormat model.APIFormat
	RequestType    model.RequestType
	Quality        ConversionQuality
}

var matrixInboundFormats = []model.APIFormat{
	model.APIFormatOpenAIChatCompletion,
	model.APIFormatOpenAIResponse,
	model.APIFormatAnthropicMessage,
	model.APIFormatGeminiContents,
	model.APIFormatOpenAIEmbedding,
	model.APIFormatOpenAIImageGeneration,
}

var matrixRequestTypes = []model.RequestType{
	model.RequestTypeChat,
	model.RequestTypeResponses,
	model.RequestTypeEmbedding,
	model.RequestTypeImages,
	model.RequestTypeRerank,
}

// StaticQualityMatrix returns the complete protocol/operation matrix. Native
// means byte-stable passthrough is available, lossless means the canonical
// shape is fully represented, and conditional means request features decide
// whether the concrete conversion is degraded.
func StaticQualityMatrix() []QualityMatrixEntry {
	types := make([]OutboundType, 0, len(protocolDescriptors))
	for typ := range protocolDescriptors {
		types = append(types, typ)
	}
	sort.Slice(types, func(i, j int) bool { return types[i] < types[j] })

	entries := make([]QualityMatrixEntry, 0, len(matrixInboundFormats)*len(types)*len(matrixRequestTypes))
	for _, inboundFormat := range matrixInboundFormats {
		for _, outboundType := range types {
			capability := protocolDescriptors[outboundType]
			for _, requestType := range matrixRequestTypes {
				entries = append(entries, QualityMatrixEntry{
					InboundFormat:  inboundFormat,
					OutboundType:   outboundType,
					OutboundFormat: capability.APIFormat,
					RequestType:    requestType,
					Quality:        StaticConversionQuality(inboundFormat, outboundType, requestType),
				})
			}
		}
	}
	return entries
}

func StaticConversionQuality(inboundFormat model.APIFormat, outboundType OutboundType, requestType model.RequestType) ConversionQuality {
	capability, ok := Descriptor(outboundType)
	if !ok || !capability.Supports(requestType) {
		return QualityUnsupported
	}
	if inboundFormat != "" && !formatCarriesOperation(inboundFormat, requestType) {
		return QualityUnsupported
	}
	if SupportsNativeFormat(outboundType, inboundFormat) {
		return QualityNative
	}
	if requestType == model.RequestTypeEmbedding &&
		inboundFormat == model.APIFormatOpenAIEmbedding &&
		capability.APIFormat == model.APIFormatOpenAIEmbedding {
		return QualityLossless
	}
	if requestType == model.RequestTypeChat || requestType == model.RequestTypeResponses {
		return QualityConditional
	}
	return QualityUnsupported
}

func formatCarriesOperation(format model.APIFormat, requestType model.RequestType) bool {
	switch requestType {
	case model.RequestTypeChat:
		return format == model.APIFormatOpenAIChatCompletion || format == model.APIFormatAnthropicMessage || format == model.APIFormatGeminiContents
	case model.RequestTypeResponses:
		return format == model.APIFormatOpenAIResponse
	case model.RequestTypeEmbedding:
		return format == model.APIFormatOpenAIEmbedding
	case model.RequestTypeImages:
		return format == model.APIFormatOpenAIImageGeneration
	default:
		return false
	}
}
