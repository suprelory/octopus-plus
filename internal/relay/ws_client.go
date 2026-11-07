package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/suprelory/octopus-plus/internal/apperror"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/internal/server/auth"
	"github.com/suprelory/octopus-plus/internal/server/middleware"
	"github.com/suprelory/octopus-plus/internal/utils/log"
)

const (
	wsClientMaxAge    = 60 * time.Minute
	wsClientReadLimit = 16 * 1024 * 1024 // 16MB per message
)

// HandleWSResponse handles WebSocket upgrade for /v1/responses.
func HandleWSResponse(c *gin.Context) {
	conn, err := websocket.Accept(c.Writer, c.Request, &websocket.AcceptOptions{
		InsecureSkipVerify: true, // Allow cross-origin
	})
	if err != nil {
		log.Warnf("websocket upgrade failed: %v", relayErrorDiagnostic(err))
		return
	}
	defer conn.CloseNow()

	conn.SetReadLimit(wsClientReadLimit)

	lifetimeCtx, cancelLifetime := context.WithTimeout(c.Request.Context(), wsClientMaxAge)
	defer cancelLifetime()
	ctx, cancelSession := context.WithCancelCause(lifetimeCtx)
	defer cancelSession(nil)
	ctx = context.WithValue(ctx, affinityHeadersKey{}, c.Request.Header.Clone())
	reader := newWSClientReader(ctx, conn, cancelSession)
	defer reader.Close()
	stopKeepAlive := startWSKeepAlive(ctx, conn, streamHeartbeatInterval(), cancelSession)
	defer stopKeepAlive()

	apiKeyID := c.GetInt("api_key_id")
	apiKey := c.GetString("authenticated_api_key")
	clientIP := middleware.ClientIP(c)

	log.Debugf("ws client connected (apikey=%d)", apiKeyID)

	downstreamSessionID := fmt.Sprintf("ws_%d", time.Now().UnixNano())
	// The session ID is unique to this connection, so any state left behind is
	// unreachable once we return. Release it here instead of waiting for the
	// sweeper to notice the TTL.
	defer deleteWSConversationStatesBySession(apiKeyID, downstreamSessionID)
	var conversationState *wsConversationState

	// Message loop
	for {
		msgType, data, err := reader.Read(ctx)
		if err != nil {
			if lifetimeCtx.Err() == context.DeadlineExceeded {
				writeCtx, cancelWrite := context.WithTimeout(context.WithoutCancel(ctx), wsWriteTimeout)
				writeWSError(writeCtx, conn, 400, "websocket_connection_limit_reached",
					"Responses websocket connection limit reached (60 minutes). Create a new websocket connection to continue.")
				cancelWrite()
				return
			}
			closeStatus := websocket.CloseStatus(err)
			if closeStatus == websocket.StatusNormalClosure || closeStatus == websocket.StatusGoingAway {
				log.Debugf("ws client disconnected normally (apikey=%d)", apiKeyID)
			} else {
				log.Warnf("ws client read error (apikey=%d): %v", apiKeyID, relayErrorDiagnostic(err))
			}
			return
		}

		if msgType != websocket.MessageText {
			continue
		}

		var msg struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			writeWSError(ctx, conn, 400, "invalid_request", "Failed to parse message")
			continue
		}

		if msg.Type != "response.create" {
			writeWSError(ctx, conn, 400, "invalid_request",
				fmt.Sprintf("Unknown message type: %s", msg.Type))
			continue
		}

		key, err := op.APIKeyGetByAPIKey(apiKey, ctx)
		if err != nil || key.ID != apiKeyID || key.APIKey != apiKey {
			middleware.RecordAuthEvent(c, "api_key.rejected", apperror.CodeAuthInvalidToken, apiKeyID)
			writeWSError(ctx, conn, http.StatusUnauthorized, apperror.CodeAuthInvalidToken, "Invalid API key")
			continue
		}
		if authErr, retryAfter := auth.ValidateAPIKey(key, true); authErr != nil {
			middleware.RecordAuthEvent(c, "api_key.rejected", authErr.Code, apiKeyID)
			var retryAt time.Time
			if retryAfter > 0 {
				retryAt = time.Now().Add(time.Duration(retryAfter) * time.Second)
			}
			writeWSError(ctx, conn, authErr.Status, authErr.Code, authErr.Message, retryAt)
			continue
		}
		reservation, authErr := auth.ReserveAPIKeyCost(key)
		if authErr != nil {
			middleware.RecordAuthEvent(c, "api_key.rejected", authErr.Code, apiKeyID)
			writeWSError(ctx, conn, authErr.Status, authErr.Code, authErr.Message, time.Now().Add(time.Second))
			continue
		}
		func() {
			defer reservation.Release()
			turnCtx := op.WithAPIKeyCostReservation(ctx, reservation)
			conversationState = processWSResponseCreate(turnCtx, conn, data, apiKeyID, key.SupportedModels, clientIP, downstreamSessionID, conversationState)
		}()
	}
}

func writeWSError(ctx context.Context, conn *websocket.Conn, status int, code, message string, retryAt ...time.Time) {
	deadline := time.Time{}
	if len(retryAt) > 0 {
		deadline = retryAt[0]
	}
	errEvent := buildWSErrorEvent(status, code, message, deadline, time.Now())
	if err := writeWSEvent(ctx, conn, errEvent); err != nil {
		log.Debugf("ws error event write failed: %v", relayErrorDiagnostic(err))
	}
}

func buildWSErrorEvent(status int, code, message string, retryAt, now time.Time) map[string]interface{} {
	errEvent := map[string]interface{}{
		"type":   "error",
		"status": status,
		"error": map[string]interface{}{
			"type":    "invalid_request_error",
			"code":    code,
			"message": message,
		},
	}
	if value := retryAfterHeaderValue(retryAt, now); value != "" {
		if seconds, err := strconv.Atoi(value); err == nil {
			errEvent["retry_after"] = seconds
		}
	}
	return errEvent
}
