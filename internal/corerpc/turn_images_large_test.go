package corerpc_test

// 大图（base64 >2MB）不再被 WS 单条消息上限掐断：
// turn.start 受理，thread.entries 能把图片 base64 原样带回来。

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// bigPNG 生成一张 base64 >2MB 的伪图片载荷（服务端只校验 base64/MIME/大小）。
func bigPNG() string {
	raw := make([]byte, 1600*1024)
	for i := range raw {
		raw[i] = byte(i % 251)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

func TestTurnStartWithLargeImagePayload(t *testing.T) {
	sess := newSession(t, nil)
	url, _ := startServer(t, sess, "secret")
	c := dial(t, url, "secret")
	threadID := createThread(t, c, "大图")

	raw := c.call(t, "turn.start", map[string]any{
		"threadId":       threadID,
		"idempotencyKey": "img-large001",
		"text":           "大图",
		"images": []map[string]any{
			{"type": "image", "mime": "image/png", "data": bigPNG()},
		},
	})
	if strings.Contains(string(raw), "error") {
		t.Fatalf("turn.start = %s", raw)
	}
	// 等任意终态；把 terminal 原因带出来（失败时能看到具体错误）。
	errEvt := c.waitEvent(t, "evt.error", "")
	var evtErr struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(errEvt.Params, &evtErr); err != nil {
		t.Fatalf("evt.error 解析: %v (%s)", err, errEvt.Params)
	}
	if evtErr.Code != 2004 {
		t.Fatalf("预期 context_overflow（说明载荷已过传输/存储上限），实得: %s", errEvt.Params)
	}

	// 用户 entry（含大图 base64）已在 begin() 落库，entries 能原样带回来。
	raw = c.call(t, "thread.entries", map[string]any{"threadId": threadID, "limit": 10})
	if !strings.Contains(string(raw), bigPNG()[:64]) {
		t.Fatalf("thread.entries 没有带出大图 base64: %.200s", raw)
	}
}
