package app

// 托盘图标：常驻通知区（Windows）或菜单栏（macOS），提供显示/隐藏窗口、复制 MCP
// 配置与退出。
//
// 窗口关闭仍然是退出应用（与之前一致）；托盘用来把窗口隐藏后继续跑控制服务，
// 再点「Show Window」回来。创建失败只记日志：没有托盘时窗口照常可用。

import (
	"encoding/json"
	"log"
	"os"
	"strings"

	"github.com/egoist/mygo"
)

// mcpConfigSnippet 返回可直接粘进 ada-go `.adago/mcp.json` servers 数组的条目。
//
// browser-use 的 MCP 以 stdio 模式跑（`browser-use mcp`）：不占端口，经由
// runtime.json 复用正在运行的应用；HTTP 模式需要单独启动
// `browser-use mcp --http --port 8766`，托盘不代管它。
func mcpConfigSnippet() string {
	exe, err := os.Executable()
	if err != nil || strings.TrimSpace(exe) == "" {
		exe = "browser-use.exe"
	}
	cfg := map[string]any{
		"id":        "browser_use",
		"transport": "stdio",
		"command":   exe,
		"args":      []string{"mcp"},
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return ""
	}
	return string(raw) + "\n"
}

// copyMCPConfig 把配置写进剪贴板，并尽力弹一条系统通知确认。
func copyMCPConfig() {
	text := mcpConfigSnippet()
	if text == "" {
		return
	}
	mygo.Clipboard.WriteText(text)
	if !mygo.NotificationsSupported() {
		return
	}
	note := mygo.NewNotification(mygo.NotificationOptions{
		Title: "browser-use",
		Body:  "MCP config copied. Paste it into .adago/mcp.json (servers).",
	})
	if err := note.Show(); err != nil {
		log.Printf("browser-use: notification: %v", err)
	}
}

// startTray 在应用就绪后创建托盘图标；失败返回 nil。
func startTray(a *App) *mygo.Tray {
	show := func(*mygo.MenuItem, *mygo.Window) {
		if a.win == nil {
			return
		}
		a.win.Show()
		a.win.Focus()
	}
	hide := func(*mygo.MenuItem, *mygo.Window) {
		if a.win != nil {
			a.win.Hide()
		}
	}
	menu := mygo.NewMenu([]*mygo.MenuItem{
		{Label: "Show Window", Click: show},
		{Label: "Hide Window", Click: hide},
		mygo.Separator(),
		{Label: "Copy MCP Config", Click: func(*mygo.MenuItem, *mygo.Window) { copyMCPConfig() }},
		mygo.Separator(),
		{Label: "Quit browser-use", Click: func(*mygo.MenuItem, *mygo.Window) { mygo.App.Quit() }},
	})
	tray, err := mygo.NewTray(mygo.TrayOptions{
		ID:      "browser-use",
		Icon:    trayIconPNG,
		ToolTip: "browser-use",
		Menu:    menu,
	})
	if err != nil {
		log.Printf("browser-use: tray: %v", err)
		return nil
	}
	return tray
}
