package relay

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/suprelory/octopus-plus/polywire/model"
)

func TestProtocolErrorForAttemptClassifiesInterruptedStream(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want error
	}{
		{name: "missing terminal event", err: fmt.Errorf("finalize: %w", model.ErrStreamIncomplete), want: model.ErrStreamIncomplete},
		{name: "wrapped eof", err: fmt.Errorf("read: %w", io.EOF), want: io.EOF},
		{name: "unexpected eof", err: fmt.Errorf("read: %w", io.ErrUnexpectedEOF), want: io.ErrUnexpectedEOF},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			responseError := protocolErrorForAttempt(attemptResult{
				StatusCode: http.StatusOK,
				Failure:    FailureClassification{Class: FailureTransient, Retryable: true},
			}, tt.err)
			if responseError == nil {
				t.Fatal("expected response error")
			}
			if responseError.StatusCode != http.StatusBadGateway || responseError.Detail.Code != CodeRelayUpstreamStreamInterrupted {
				t.Fatalf("response error = %+v", responseError)
			}
			if !errors.Is(tt.err, tt.want) {
				t.Fatalf("test error does not preserve %v", tt.want)
			}
		})
	}
}
