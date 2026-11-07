package relay

import "github.com/suprelory/octopus-plus/polywire/inbound"

func relayEndpointType(inboundType inbound.InboundType) string {
	switch inboundType {
	case inbound.InboundTypeOpenAIResponse:
		return "responses"
	case inbound.InboundTypeAnthropic:
		return "messages"
	case inbound.InboundTypeOpenAIEmbedding:
		return "embeddings"
	default:
		return "chat"
	}
}
