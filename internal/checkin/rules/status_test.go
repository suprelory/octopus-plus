package rules

import (
	"context"
	"net/http"
	"testing"

	"github.com/suprelory/octopus-plus/internal/model"
)

func TestCheckinStatusRuleEmptyResponseUsesConfiguredScript(t *testing.T) {
	config := &model.SiteCheckinStatusConfig{Code: `return context.httpStatus === 204 && Object.keys(response).length === 0 ? "checked_in" : "unknown";`}
	state, err := Evaluate(context.Background(), config, &Response{StatusCode: http.StatusNoContent}, "2026-11-17")
	if err != nil || state != Checked {
		t.Fatalf("empty response no longer supports an explicit status script: state=%s err=%v", state, err)
	}
	for _, body := range []string{"null", `<html>challenge</html>`} {
		state, err := Evaluate(context.Background(), config, &Response{StatusCode: http.StatusNoContent, Body: []byte(body)}, "2026-11-17")
		if err != nil || state != Unknown {
			t.Fatalf("non-object response was accepted by the script: state=%s err=%v", state, err)
		}
	}
}

func TestCheckinRewardTrimsResponseStringAndPreservesZero(t *testing.T) {
	for _, source := range []string{"submit", "status"} {
		response := &Response{StatusCode: http.StatusOK, Body: []byte(`{"data":{"reward":" 0 "}}`)}
		var submit, status *Response
		if source == "submit" {
			submit = response
		} else {
			status = response
		}
		if reward := Reward(context.Background(), "", source, submit, status); reward != "0" {
			t.Fatalf("%s reward lost string normalization or zero: %q", source, reward)
		}
	}
}
