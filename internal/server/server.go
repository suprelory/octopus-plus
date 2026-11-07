package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/gin-contrib/gzip"
	"github.com/gin-gonic/gin"
	"github.com/suprelory/octopus-plus/internal/conf"
	"github.com/suprelory/octopus-plus/internal/relay/bodycache"
	_ "github.com/suprelory/octopus-plus/internal/server/handlers"
	"github.com/suprelory/octopus-plus/internal/server/middleware"
	"github.com/suprelory/octopus-plus/internal/server/resp"
	"github.com/suprelory/octopus-plus/internal/server/router"
	"github.com/suprelory/octopus-plus/internal/utils/log"
	"github.com/suprelory/octopus-plus/internal/utils/safe"
	"github.com/suprelory/octopus-plus/static"
)

var httpSrv http.Server

const (
	// readHeaderTimeout 限制读完请求头的时间，防 Slowloris（gosec G112）。
	readHeaderTimeout = 15 * time.Second
	// idleTimeout 限制 keep-alive 连接的空闲时长。
	idleTimeout = 60 * time.Second
	// shutdownGracePeriod 是优雅关闭等待在途请求的上限。docker stop 默认给
	// 10s，其中 cmd.ensureIndexShutdownGrace 已占最多 3s，后面还有 relay log
	// flush 与 db.Close，所以这里只取 5s。跑得更久的流式请求仍会被截断，
	// 但至少不再是所有连接一起硬切。
	shutdownGracePeriod = 5 * time.Second
)

// 有意不设置 ReadTimeout / WriteTimeout：SSE 流式响应与 WebSocket 长连接
// 会被这两个超时直接截断。

func Start() error {
	if conf.IsDebug() {
		gin.SetMode(gin.DebugMode)
	} else {
		gin.SetMode(gin.ReleaseMode)
	}

	// 启动时清理 Images 请求体临时文件（失败仅告警，不阻断启动）
	tmpDir := bodycache.TmpDirFromEnv()
	olderThan := bodycache.TmpCleanupOlderThanFromEnv()
	if err := bodycache.CleanupOldTmpFiles(tmpDir, bodycache.TmpFilePrefix, olderThan); err != nil {
		log.Warnf("cleanup images tmp files failed: dir=%s prefix=%s olderThan=%s err=%v", tmpDir, bodycache.TmpFilePrefix, olderThan, err)
	}

	r := gin.New()
	// Client IP resolution is handled by the runtime setting middleware. Keep
	// Gin's own proxy parser disabled so a stale static config cannot bypass it.
	if err := r.SetTrustedProxies(nil); err != nil {
		return fmt.Errorf("disable Gin trusted proxy parser: %w", err)
	}
	if err := middleware.ReloadTrustedProxies(); err != nil {
		// Fail closed on a malformed value imported from an older/manual backup:
		// keep the service available, but trust no forwarded headers until an
		// administrator corrects the setting.
		log.Warnf("trusted proxy setting is invalid; forwarded headers will be ignored: %v", err)
		if resetErr := middleware.ConfigureTrustedProxies(""); resetErr != nil {
			return fmt.Errorf("reset trusted proxy resolver: %w", resetErr)
		}
	}
	if middleware.TrustedProxyCount() == 0 {
		log.Infof("no trusted proxies configured: client IP comes from the TCP peer address, forwarded headers are ignored")
	} else {
		log.Infof("trusted proxy resolution enabled for %d configured IP/CIDR entries", middleware.TrustedProxyCount())
	}

	r.Use(gin.CustomRecovery(func(c *gin.Context, recovered interface{}) {
		log.Errorf("http panic recovered: %v", recovered)
		resp.Error(c, http.StatusInternalServerError, resp.ErrInternalServer)
		c.Abort()
	}))

	r.Use(gzip.Gzip(gzip.DefaultCompression,
		gzip.WithExcludedPaths([]string{"/v1/"}),
		gzip.WithExcludedPathsRegexs([]string{`/api/v1/log/stream`}),
	))

	r.Use(middleware.Logger(middleware.LoggerConfig{
		Enabled:       conf.AppConfig.Log.Access.Enabled || conf.IsDebug(),
		SlowThreshold: time.Duration(conf.AppConfig.Log.Access.SlowThresholdMS) * time.Millisecond,
	}))
	r.Use(middleware.Cors())
	r.Use(middleware.MaxBodySize())
	r.Use(middleware.StaticEmbed("/", static.StaticFS))

	addr := fmt.Sprintf("%s:%d", conf.AppConfig.Server.Host, conf.AppConfig.Server.Port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}
	if err := router.RegisterAll(r); err != nil {
		_ = listener.Close()
		return fmt.Errorf("register routes: %w", err)
	}

	httpSrv = http.Server{
		Addr:              addr,
		Handler:           r,
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
	}
	safe.Go("http-listen", func() {
		if err := httpSrv.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Errorf("http server listen and serve error: %v", err)
		}
	})
	return nil
}

// Close 优雅关闭 HTTP server：停止接受新连接，给在途请求 shutdownGracePeriod
// 的时间收尾，超时后再强制断开。
func Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownGracePeriod)
	defer cancel()

	if err := httpSrv.Shutdown(ctx); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			log.Warnf("http server graceful shutdown timed out after %s, forcing close", shutdownGracePeriod)
			return httpSrv.Close()
		}
		return err
	}
	return nil
}
