package host

// 宿主内部环境变量不得随继承路径泄露给插件子进程（09 §9.2）。
//
// 这条边界与 manifest 的 backend.env 前缀规则是两件事：那条规则只拦「显式声明」，
// 而 child_process / 后台任务 / process.start 在没给显式 env 时会克隆整个宿主环境。

import (
	"strings"
	"testing"
)

func TestStripHostEnvRemovesInternalNamespaces(t *testing.T) {
	t.Parallel()
	in := []string{
		"ADAG_CORE_TOKEN=secret",
		"ADAGO_GATEWAY_CONTROL_TOKEN=secret",
		"ADAGO_HOME=/home/user",
		"PATH=/usr/bin",
		"HOME=/home/user",
		"ADA_PROBE_ENV=managed",
		"=broken",
	}
	out := stripHostEnv(in)
	joined := strings.Join(out, "\n")
	for _, gone := range []string{"ADAG_CORE_TOKEN", "ADAGO_GATEWAY_CONTROL_TOKEN", "ADAGO_HOME"} {
		if strings.Contains(joined, gone) {
			t.Fatalf("%s 不该传给插件子进程:\n%s", gone, joined)
		}
	}
	for _, keep := range []string{"PATH=/usr/bin", "HOME=/home/user", "ADA_PROBE_ENV=managed"} {
		if !strings.Contains(joined, keep) {
			t.Fatalf("%s 应保留:\n%s", keep, joined)
		}
	}
	if len(out) != 3 {
		t.Fatalf("剩余 %d 项，期望 3（非法项也一并丢弃）:\n%s", len(out), joined)
	}
	// 大小写不敏感（Windows 环境变量名不区分大小写）。
	if got := stripHostEnv([]string{"adag_core_token=x", "keep=1"}); len(got) != 1 {
		t.Fatalf("小写前缀也应被摘除: %v", got)
	}
	// 空环境块不 panic，也不产生错位。
	if got := stripHostEnv(nil); len(got) != 0 {
		t.Fatalf("nil 输入 = %v", got)
	}
}
