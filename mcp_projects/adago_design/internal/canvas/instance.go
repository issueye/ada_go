// 同机多实例探测：两个进程同时打开同一份数据目录时，各自内存态 +
// 全量覆盖写 canvas.json，后写覆盖先写，会静默丢节点。
// 写入路径只有数据层（Store），但「第二个进程根本不该打开这份数据」
// 拦在进程启动处更早 —— 这里在绑定端口前先探测同数据目录的存量实例。
package canvas

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Health 是 /api/health 的应答形状（探测用子集）。
type Health struct {
	OK      bool   `json:"ok"`
	Server  string `json:"server"`
	DataDir string `json:"data_dir"`
}

// ProbeInstance 在 port..port+20 范围内探测本机是否已有**同一数据目录**的
// adago-design 实例在运行。返回命中实例的 BaseURL。
//
// 只把「同数据目录」当冲突：不同数据目录的实例各管各的画布，可以共存
// （端口探测会自动 +1 错开）。health 端点回显 server/data_dir 就是给这里用的。
func ProbeInstance(port int, dataDir string) (baseURL string, ok bool) {
	client := &http.Client{Timeout: 600 * time.Millisecond}
	for p := port; p <= port+20; p++ {
		url := fmt.Sprintf("http://127.0.0.1:%d/api/health", p)
		resp, err := client.Get(url)
		if err != nil {
			continue // 没人监听 / 不是 HTTP 服务：快速失败，继续下一个端口
		}
		var h Health
		err = json.NewDecoder(resp.Body).Decode(&h)
		_ = resp.Body.Close()
		if err != nil || !h.OK || h.Server != "adago-design" {
			continue
		}
		if sameDataDir(h.DataDir, dataDir) {
			return fmt.Sprintf("http://127.0.0.1:%d", p), true
		}
	}
	return "", false
}

// sameDataDir 归一化后比较两个数据目录。Windows 路径不区分大小写。
func sameDataDir(a, b string) bool {
	a, b = filepath.Clean(strings.TrimSpace(a)), filepath.Clean(strings.TrimSpace(b))
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}
