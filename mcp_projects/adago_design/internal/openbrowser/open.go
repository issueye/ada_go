// Package openbrowser 以系统默认浏览器打开画布地址（尽力而为）。
package openbrowser

import (
	"os"
	"os/exec"
	"runtime"
)

// Open 尝试用默认浏览器打开 url；失败不报错（用户可手动打开）。
func Open(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		// start 会把带 & 的 URL 截断，包一层 cmd /c start "" "url"
		cmd = exec.Command("cmd", "/c", "start", "", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if v := os.Getenv("ADAGO_DESIGN_BROWSER"); v != "" {
		switch runtime.GOOS {
		case "windows":
			cmd = exec.Command("cmd", "/c", "start", "", v, url)
		case "darwin":
			cmd = exec.Command("open", "-a", v, url)
		default:
			cmd = exec.Command(v, url)
		}
	}
	if err := cmd.Start(); err == nil {
		go func() { _ = cmd.Wait() }()
	}
}
