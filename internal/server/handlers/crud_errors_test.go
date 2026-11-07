package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/suprelory/octopus-plus/internal/apperror"
)

func TestMutationErrorStatus(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "unsupported operation error", err: errors.New("unsupported channel type: 4"), want: http.StatusBadRequest},
		{name: "wrapped unsupported operation error", err: fmt.Errorf("create failed: %w", errors.New("unsupported channel type: 4")), want: http.StatusBadRequest},
		{name: "not found", err: errors.New("channel not found"), want: http.StatusNotFound},
		{name: "app error status wins", err: apperror.New("channel.conflict", "conflict").WithStatus(http.StatusConflict), want: http.StatusConflict},
		{name: "unexpected error", err: errors.New("database unavailable"), want: http.StatusInternalServerError},
		{name: "nil", err: nil, want: http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mutationErrorStatus(tt.err); got != tt.want {
				t.Fatalf("mutationErrorStatus(%v) = %d, want %d", tt.err, got, tt.want)
			}
		})
	}
}

func TestChannelErrorUsesMutationStatus(t *testing.T) {
	err := channelError("channel.create_failed", "channel create failed", errors.New("unsupported channel type: 4"))
	if err.Status != http.StatusBadRequest {
		t.Fatalf("channelError status = %d, want %d", err.Status, http.StatusBadRequest)
	}
}
