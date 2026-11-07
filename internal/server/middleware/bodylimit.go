package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/suprelory/octopus-plus/internal/conf"
)

// importBodyLimitMultiplier 放宽数据导入端点的上限。这些端点上传的是数据库
// 快照，体积随部署规模变化，套基础上限会误伤正常导入；但仍要有界，避免一个
// 损坏的上传把内存吃光。
const importBodyLimitMultiplier = 16

// importPathPrefixes 是走「宽松上限」的导入端点。都需要管理员 JWT。
var importPathPrefixes = []string{
	"/api/v1/setting/import",
	"/api/v1/site/import",
}

// imagesPathPrefix 下的请求体由 relay.ImagesHandler 交给 bodycache 处理：
// 它自己有上限（OCTOPUS_IMAGES_BODY_MAX_MB，默认 256 MB），并且超过内存阈值
// 就落盘，不会把整个 body 驻留内存。这里再包一层只会重复限制。
const imagesPathPrefix = "/v1/images"

// MaxBodySize 给请求体套 http.MaxBytesReader，避免持有有效凭证的调用方用超大
// body 打爆内存。超限时读取方拿到的是 *http.MaxBytesError，由各自的 handler
// 翻译成 413。
//
// 这里只包 Body。不要顺手加 ReadTimeout / WriteTimeout —— SSE 流式响应和
// WebSocket 长连接会被那两个超时直接截断。
func MaxBodySize() gin.HandlerFunc {
	base := conf.MaxRequestBodyBytes()
	importLimit := base * importBodyLimitMultiplier

	return func(c *gin.Context) {
		limit := bodyLimitForPath(c.Request.URL.Path, base, importLimit)
		if limit <= 0 || c.Request.Body == nil {
			c.Next()
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
		c.Next()
	}
}

func bodyLimitForPath(path string, base, importLimit int64) int64 {
	if strings.HasPrefix(path, imagesPathPrefix) {
		return 0
	}
	for _, prefix := range importPathPrefixes {
		if strings.HasPrefix(path, prefix) {
			return importLimit
		}
	}
	return base
}
