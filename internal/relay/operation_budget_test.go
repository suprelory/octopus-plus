package relay

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	dbmodel "github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"github.com/suprelory/octopus-plus/polywire/outbound"
)

func TestOperationHandlersShareSubmissionAndChannelBudgets(t *testing.T) {
	for _, operation := range []string{"images", "compact"} {
		for _, limit := range []string{"max_total_attempts", "max_channel_attempts"} {
			t.Run(operation+"/"+limit, func(t *testing.T) {
				ctx := setupHTTPRelayTestDB(t)
				if err := op.SettingSetInt(dbmodel.SettingKey("relay_"+operation+"_"+limit), 1); err != nil {
					t.Fatal(err)
				}
				var hits atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					hits.Add(1)
					w.WriteHeader(http.StatusBadGateway)
					_, _ = io.WriteString(w, `{"error":{"message":"upstream unavailable"}}`)
				}))
				defer server.Close()
				group := &dbmodel.Group{Name: "budget-test", Mode: dbmodel.GroupModeFailover}
				channels := addHTTPRelayTestChannels(t, ctx, group, dbmodel.ChannelPassthroughModeOff, server.URL, server.URL)
				if operation == "images" {
					for _, channel := range channels {
						typ := outbound.OutboundTypeOpenAIChat
						if _, err := op.ChannelUpdate(&dbmodel.ChannelUpdateRequest{ID: channel.ID, Type: &typ}, ctx); err != nil {
							t.Fatal(err)
						}
					}
				}
				c, recorder := newHTTPRelayTestContext(ctx, `{"model":"budget-test","input":"hello","prompt":"hello"}`)
				if operation == "images" {
					ImagesHandler("/images/generations", c)
				} else {
					HandleResponsesCompact(c)
				}
				if hits.Load() != 1 || recorder.Code != http.StatusGatewayTimeout || !strings.Contains(recorder.Body.String(), CodeRelayTimeout) {
					t.Fatalf("hits=%d status=%d body=%s", hits.Load(), recorder.Code, recorder.Body.String())
				}
				assertHTTPRelayKeysReleased(t, channels)
			})
		}
	}
}

func TestImagesPrecommitBudgetCoversHeadersAndSurvivesCommit(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(fmt.Sprint(committed), func(t *testing.T) {
			_ = setupHTTPRelayTestDB(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if committed {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: {\"type\":\"image_generation.partial_image\",\"b64_json\":\"a\"}\n\n")
					w.(http.Flusher).Flush()
				}
				select {
				case <-time.After(250 * time.Millisecond):
				case <-r.Context().Done():
					return
				}
				_, _ = io.WriteString(w, "data: {\"type\":\"image_generation.completed\",\"b64_json\":\"b\"}\n\n")
			}))
			defer server.Close()
			channel := &dbmodel.Channel{ID: 11, BaseUrls: []dbmodel.BaseUrl{{URL: server.URL}}}
			e := newRelayExecution(dbmodel.Group{}, true)
			e.budget.deadline = time.Now().Add(150 * time.Millisecond)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/images/generations", nil)
			_, written, _, _, err := imagesAttempt(context.Background(), "/images/generations", c, nil, false, "",
				map[string]any{"model": "image"}, true, channel, "key", 0, newImagesRelayMetrics(0, "image", ""), "image", nil, nil, e)
			if committed {
				if err != nil || !written {
					t.Fatalf("committed stream: written=%t err=%v", written, err)
				}
			} else if !isLocalRelayBudgetError(err) || written {
				t.Fatalf("precommit timeout: written=%t err=%v", written, err)
			}
			if e.budget.totalAttempts != 1 {
				t.Fatalf("sends=%d", e.budget.totalAttempts)
			}
		})
	}
}
