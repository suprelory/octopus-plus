package handlers

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/suprelory/octopus-plus/internal/notify"
	"github.com/suprelory/octopus-plus/internal/server/middleware"
	"github.com/suprelory/octopus-plus/internal/server/resp"
	"github.com/suprelory/octopus-plus/internal/server/router"
)

var notificationTestClient = notify.NewHTTPClient()

func init() {
	router.NewGroupRouter("/api/v1/setting").
		Use(middleware.Auth()).
		AddRoute(router.NewRoute("/notification/test", http.MethodPost).
			Use(middleware.RequireJSON()).Handle(testNotificationChannel))
}

// Tests use the supplied draft and do not save settings or consume the
// check-in cooldown. The administrator explicitly selects one destination.
func testNotificationChannel(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 65536)
	var request struct {
		Channel notify.Kind   `json:"channel"`
		Config  notify.Config `json:"config"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		resp.Error(c, http.StatusBadRequest, "invalid notification test request")
		return
	}
	if err := request.Config.Validate(); err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	middleware.AuditFields(c, "notification_channel", string(request.Channel))
	for _, target := range request.Config.Targets() {
		if target.Kind != request.Channel {
			continue
		}
		err := notify.Deliver(c.Request.Context(), notificationTestClient, target, notify.Message{
			Level: "info", Title: "Octopus 通知测试", Text: "通知渠道连接成功。签到结果将通过此渠道发送。",
			Timestamp: time.Now().UTC(),
			Variables: map[string]string{
				"event": "通知测试", "site": "示例站点", "account": "示例账号", "source": "手动测试",
				"detail": "通知渠道连接成功。", "reward": "0.50 USD", "balance": "12.5000", "threshold": "5.0000", "failure_count": "0",
			},
		})
		if err != nil {
			resp.Error(c, http.StatusBadGateway, err.Error())
			return
		}
		resp.Success(c, gin.H{"channel": target.Kind, "success": true})
		return
	}
	resp.Error(c, http.StatusBadRequest, "notification channel is not configured")
}
