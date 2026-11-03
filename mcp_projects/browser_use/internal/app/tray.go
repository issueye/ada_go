package app

// 托盘图标：常驻通知区（Windows）或菜单栏（macOS），提供显示/隐藏窗口与退出。
//
// 窗口关闭仍然是退出应用（与之前一致）；托盘用来把窗口隐藏后继续跑控制服务，
// 再点「Show Window」回来。创建失败只记日志：没有托盘时窗口照常可用。

import (
	"log"

	"github.com/egoist/mygo"
)

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
