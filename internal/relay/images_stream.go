package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/suprelory/octopus-plus/internal/relay/stream"
)

// proxySSE 将上游 SSE 逐行解析 event/data/空行并透传到下游；首事件计为 FirstTokenTime；支持 FirstTokenTimeOut 切换。
func proxySSE(ctx context.Context, c *gin.Context, respUp *http.Response, firstTokenTimeOutSec int, metrics *imagesRelayMetrics, hb *earlyHeartbeat, onCommit ...func()) (*imagesUsage, bool, error) {
	if ct := respUp.Header.Get("Content-Type"); ct != "" && !strings.Contains(strings.ToLower(ct), "text/event-stream") {
		b, _ := io.ReadAll(io.LimitReader(respUp.Body, imagesUpstreamErrorBodyLimit))
		return nil, false, fmt.Errorf("upstream returned non-SSE content-type %q for stream request: %s", ct, string(b))
	}

	hb.Hand()
	scanner := newUsageScanner()
	semantic := false
	observer := stream.NewIncrementalSSEObserver(maxSSEEventSize, nil, func(_ context.Context, eventType string, data []byte) error {
		if eventType == "error" {
			return fmt.Errorf("upstream image stream error: %s", data)
		}
		if eventType == "image_generation.completed" || eventType == "image_generation.partial_image" ||
			bytes.Contains(data, []byte(`"b64_json"`)) {
			semantic = true
		}
		scanner.Feed(data)
		return nil
	})
	committed := false
	processor := stream.NewStreamProcessor(hb.streamConfig(stream.StreamConfig{
		Source: stream.NewRawSource(respUp.Body, 32*1024), Observer: observer,
		Writer: c.Writer, Context: ctx,
		FirstTokenTimeout:  time.Duration(firstTokenTimeOutSec) * time.Second,
		HeartbeatInterval:  streamHeartbeatInterval(),
		PrecommitPredicate: func(_, _ []byte) bool { return semantic },
		PrecommitMaxBytes:  maxSSEEventSize + 64*1024,
		PrecommitMaxEvents: maxSSEEventSize/(32*1024) + 8,
		OnCommit: func() {
			committed = true
			for _, commit := range onCommit {
				commit()
			}
		},
		OnFirstToken: func() { metrics.SetFirstTokenTime(time.Now()) },
	}))
	err := processor.Run()
	if err != nil && context.Cause(ctx) != nil {
		err = context.Cause(ctx)
	}
	return scanner.Usage(), committed, err
}

type usageScanner struct {
	matchIdx       int
	waitForObject  bool
	collecting     bool
	braceDepth     int
	inString       bool
	escape         bool
	buf            bytes.Buffer
	usage          *imagesUsage
	done           bool
	maxCollectSize int
}

func newUsageScanner() *usageScanner {
	return &usageScanner{maxCollectSize: 64 * 1024}
}

// Feed 逐字节扫描输入，定位 "usage":{...} 并仅解析 usage 子对象。
// 该实现用于避免整体 json.Unmarshal 造成 b64_json 巨大内存分配。
func (s *usageScanner) Feed(p []byte) {
	if s.done || len(p) == 0 {
		return
	}
	const pat = `"usage":`

	for _, b := range p {
		if s.done {
			return
		}

		if s.collecting {
			if s.buf.Len() >= s.maxCollectSize {
				s.collecting = false
				s.done = true
				return
			}
			s.buf.WriteByte(b)

			if s.inString {
				if s.escape {
					s.escape = false
				} else if b == '\\' {
					s.escape = true
				} else if b == '"' {
					s.inString = false
				}
				continue
			}

			if b == '"' {
				s.inString = true
				continue
			}

			switch b {
			case '{':
				s.braceDepth++
			case '}':
				s.braceDepth--
				if s.braceDepth == 0 {
					var u imagesUsage
					if err := json.Unmarshal(s.buf.Bytes(), &u); err == nil {
						s.usage = &u
					}
					s.done = true
					s.collecting = false
					return
				}
			}
			continue
		}

		if s.waitForObject {
			if b == '{' {
				s.collecting = true
				s.braceDepth = 1
				s.buf.Reset()
				s.buf.WriteByte('{')
				s.inString = false
				s.escape = false
				s.waitForObject = false
				continue
			}
			// 跳过空白，遇到其他字符则放弃
			if b == ' ' || b == '\t' || b == '\n' || b == '\r' {
				continue
			}
			s.waitForObject = false
			continue
		}

		// 匹配 "usage":
		if b == pat[s.matchIdx] {
			s.matchIdx++
			if s.matchIdx == len(pat) {
				s.waitForObject = true
				s.matchIdx = 0
			}
			continue
		}

		// 失败回退：若当前字符可能是 pat[0]，则 matchIdx=1
		if b == pat[0] {
			s.matchIdx = 1
		} else {
			s.matchIdx = 0
		}
	}
}

func (s *usageScanner) Usage() *imagesUsage {
	return s.usage
}
