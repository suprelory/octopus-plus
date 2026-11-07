package relay

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	dbmodel "github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/relay/balancer"
)

type wsRelayResult struct {
	Success           bool
	ResponseID        string
	ResetConversation bool
	Written           bool
	Canceled          bool
	Err               error
	Failure           FailureClassification
	RetryAt           time.Time
	PublicError       *wsPublicError
	Attempt           *relayRequest
}

func runWSRelay(ctx context.Context, req *relayRequest, group *dbmodel.Group, emptyResponseDetection bool) wsRelayResult {
	if req == nil || req.iter == nil {
		return wsRelayResult{Err: fmt.Errorf("relay request iterator is nil")}
	}
	if req.execution == nil {
		req.execution = newRelayExecution(*group, emptyResponseDetection)
	}
	if req.internalRequest.IsOpenAIExactReplayRequest() && req.execution.replayBudget == nil {
		if err := req.execution.beginReplay(time.Now()); err != nil {
			req.iter.Close()
			return wsResultFromAttempt(req, relayBudgetAttemptResult(err))
		}
	}
	if req.internalRequest.IsOpenAIExactReplayRequest() {
		req.metrics.SetWSMode(dbmodel.RelayLogWSModeReplay)
		req.metrics.SetWSRecovery(dbmodel.RelayLogWSRecoveryReplay)
	}
	req.ctx = ctx
	outcome := (&relayExecutor{request: req}).run()
	result := wsResultFromAttempt(req, outcome.result)
	result.Attempt = outcome.attempt
	return result
}

func wsResultFromAttempt(req *relayRequest, result attemptResult) wsRelayResult {
	wsResult := wsRelayResult{
		Success: result.Success, Written: result.Written || req.responseCommitted(), Canceled: result.Canceled,
		ResetConversation: result.ResetConversation, Err: result.Err, Failure: result.Failure, RetryAt: result.RetryAt,
	}
	if result.Success {
		if req.metrics.InternalResponse != nil {
			wsResult.ResponseID = req.metrics.InternalResponse.ID
		}
		return wsResult
	}
	if result.Failure.Class == FailureBudgetExceeded {
		return wsResult
	}
	var budgetErr *costBudgetError
	if errors.As(result.Err, &budgetErr) {
		wsResult.PublicError = &wsPublicError{Status: budgetErr.response.StatusCode, Code: budgetErr.response.Detail.Code, Message: budgetErr.response.Detail.Message}
		return wsResult
	}
	if result.StatusCode == http.StatusBadRequest && result.ProtocolError != nil {
		code := result.ProtocolError.Detail.Code
		if code == CodeRelayModelNotSupported || code == CodeRelayCapabilityRejected {
			wsResult.PublicError = &wsPublicError{Status: http.StatusBadRequest, Code: code, Message: result.ProtocolError.Detail.Message}
			return wsResult
		}
	}
	if publicErr, ok := classifyWSPublicError(result.Err, result.StatusCode); ok {
		wsResult.PublicError = &publicErr
		wsResult.ResetConversation = publicErr.ResetConversation
	}
	return wsResult
}

func finalizeWSRelay(ctx context.Context, conn *websocket.Conn, req *relayRequest, result wsRelayResult) wsRelayResult {
	if req.metrics.capture != nil {
		ctx = context.WithValue(ctx, relayCaptureKey{}, req.metrics.capture)
	}
	defer func() {
		if writer, ok := req.streamWriter.(*WSStreamWriter); ok {
			writer.finishCapture(ctx.Err() == nil)
		}
		req.metrics.SaveWithChannelStats(ctx, result.Success, result.Err, req.attempts(), false)
	}()
	if result.Success || result.Canceled || result.Written || req.responseCommitted() {
		return result
	}
	if result.PublicError != nil {
		if result.PublicError.ResetConversation {
			balancer.DeleteRoutingAffinity(req.apiKeyID, req.groupID, req.requestModel)
		}
		writeWSError(ctx, conn, result.PublicError.Status, result.PublicError.Code, result.PublicError.Message, result.RetryAt)
		return result
	}
	if result.Failure.Class != FailureNone && result.Failure.Class != "" {
		status, code := defaultFailureProtocol(result.Failure.Class, result.Failure.StatusCode)
		message := "All channels failed"
		if result.Err != nil && strings.TrimSpace(result.Err.Error()) != "" {
			message = result.Err.Error()
		}
		writeWSError(ctx, conn, status, code, message, result.RetryAt)
		return result
	}
	writeWSError(ctx, conn, 502, "all_channels_failed", "All channels failed")
	return result
}

func finalChannelKey(attempts []dbmodel.ChannelAttempt) (int, int) {
	var lastChannelID, lastChannelKeyID int
	for i := len(attempts) - 1; i >= 0; i-- {
		attempt := attempts[i]
		if attempt.Status == dbmodel.AttemptSuccess {
			return attempt.ChannelID, attempt.ChannelKeyID
		}
		if attempt.Status == dbmodel.AttemptFailed && lastChannelID == 0 {
			lastChannelID, lastChannelKeyID = attempt.ChannelID, attempt.ChannelKeyID
		}
	}
	return lastChannelID, lastChannelKeyID
}
