package cli_test

// 全局插件根（~/.adago/plugins）进入发现视野后，测试必须与真实主目录隔离：
// 否则开发机上用户安装的插件会混进每个会话的装配与工具目录，测试互相污染。

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "adago-cli-test-home-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(home)
	_ = os.Setenv("HOME", home)
	_ = os.Setenv("USERPROFILE", home)
	code := m.Run()
	_ = os.Unsetenv("HOME")
	_ = os.Unsetenv("USERPROFILE")
	os.RemoveAll(home)
	os.Exit(code)
}
