// Package config 装配 adago-design 的运行配置。
package config

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Version 是 adago-design 程序版本。
const Version = "0.1.0"

// Config 是进程级配置。
type Config struct {
	Port    int    // 画布 HTTP 端口；8788 默认，被占用自动 +1 探测
	DataDir string // 数据目录（canvas.json 与导出 HTML）
	Open    bool   // 启动后自动打开系统浏览器
	Theme   string // 画布界面默认主题 dark|light
}

// DefaultDataDir 返回默认数据目录：%APPDATA%\adago-design（可用环境变量覆盖）。
func DefaultDataDir() string {
	if v := os.Getenv("ADAGO_DESIGN_DATA"); v != "" {
		return v
	}
	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		if runtime.GOOS == "windows" {
			return `C:\ProgramData\adago-design`
		}
		return filepath.Join(os.TempDir(), "adago-design")
	}
	return filepath.Join(base, "adago-design")
}

// HasOpenFlag 判断参数里是否显式出现 -open。
func HasOpenFlag(args []string) bool {
	for _, a := range args {
		if a == "-open" || a == "--open" || strings.HasPrefix(a, "-open=") || strings.HasPrefix(a, "--open=") {
			return true
		}
	}
	return false
}

// FromFlags 从参数解析配置（子命令后缀的参数）。
func FromFlags(name string, args []string) (*Config, error) {
	cfg := &Config{}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.IntVar(&cfg.Port, "port", 8788, "画布 HTTP 端口")
	dataDir := fs.String("data", "", "数据目录（默认 %APPDATA%\\adago-design，可被 ADAGO_DESIGN_DATA 覆盖）")
	fs.BoolVar(&cfg.Open, "open", os.Getenv("ADAGO_DESIGN_OPEN") == "1", "启动后打开系统默认浏览器")
	fs.Bool("no-open", false, "serve 模式下不自动打开浏览器（等价 -open=false）")
	theme := fs.String("theme", "light", "画布界面主题: light（纸面，缺省）|dark（墨纸）")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if *dataDir != "" {
		cfg.DataDir = *dataDir
	} else {
		cfg.DataDir = DefaultDataDir()
	}
	// 主题白名单收敛：只接受 light/dark，其余按缺省（纸面）处理。
	// 注意：用户手动切换会写到 localStorage 并在页面 bootstrap 时覆盖此缺省。
	if strings.EqualFold(strings.TrimSpace(*theme), "dark") {
		cfg.Theme = "dark"
	} else {
		cfg.Theme = "light"
	}
	if cfg.Port < 0 || cfg.Port > 65535 {
		return nil, fmt.Errorf("端口 %d 超出范围", cfg.Port)
	}
	return cfg, nil
}
