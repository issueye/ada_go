package corerpc_test

// 全局插件根（~/.adago/plugins）进入发现视野后，测试必须与真实主目录隔离：
// 否则开发机上用户安装的插件会混进每个会话的装配与 system.hello 插件列表。

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "adago-corerpc-test-home-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(home)
	_ = os.Setenv("HOME", home)
	_ = os.Setenv("USERPROFILE", home)
	// 供应商播种按「已有凭据」门控；给一个假密钥，让 model.list 的断言
	// 不依赖开发机真实 secrets.toml（Fake 模式不会真的出网）。
	_ = os.Setenv("ADAG_SECRET_OPENAI_API_KEY", "test-seed-key")
	code := m.Run()
	_ = os.Unsetenv("HOME")
	_ = os.Unsetenv("USERPROFILE")
	_ = os.Unsetenv("ADAG_SECRET_OPENAI_API_KEY")
	os.RemoveAll(home)
	os.Exit(code)
}
