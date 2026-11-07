package relay

import (
	"strings"
	"testing"

	"github.com/suprelory/octopus-plus/polywire/model"
)

func TestReplayRequestUpdatesOperationPayload(t *testing.T) {
	previous, prior, next := "resp_previous", "earlier", "next"
	state := &wsConversationState{Transcript: []model.Message{{Role: "user", Content: model.MessageContent{Content: &prior}}}}
	req := &model.InternalLLMRequest{Model: "m", RawAPIFormat: model.APIFormatOpenAIResponse, Operation: &model.RequestOperation{Responses: &model.ResponsesOperation{Messages: []model.Message{{Role: "user", Content: model.MessageContent{Content: &next}}}, PreviousResponseID: &previous}}}
	replayed := state.BuildReplayRequest(req)
	if replayed == nil {
		t.Fatal("operation-only request could not be replayed")
	}
	if err := replayed.Validate(); err != nil {
		t.Fatal(err)
	}
	if replayed.OpenAIPreviousResponseID() != "" || !strings.Contains(string(replayed.ResponsesPayload().RawInputItems), "earlier") || !strings.Contains(string(replayed.OpenAIRawInputItems()), "next") {
		t.Fatalf("incorrect replay payload: %+v", replayed.ResponsesPayload())
	}
	if req.OpenAIPreviousResponseID() != previous || len(req.OpenAIRawInputItems()) != 0 {
		t.Fatal("replay mutated original request")
	}
}
