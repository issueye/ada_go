package app

import (
	"context"
	"strings"

	"browseruse/internal/audit"
	"browseruse/internal/browser"

	"github.com/egoist/mygo/ui"
)

func (a *App) view(c *ui.Context) {
	th := c.Theme()
	if a.mgr == nil {
		ui.Column(c).Fill().Background(th.Background)
		return
	}
	ui.Column(c).Fill().Background(th.Background).Children(func() {
		a.titleBar(c)
		ui.Divider(c)
		a.toolbar(c)
		ui.Divider(c)
		a.page(c)
		ui.Divider(c)
		a.statusBar(c)
	})
}

// ---- tabs ----------------------------------------------------------------

func chromeTop(c *ui.Context) float32 {
	bar := c.TitleBar()
	h := bar.Height
	if h < 36 {
		h = 36
	}
	return h + 1 // the divider under the title bar
}

func (a *App) titleBar(c *ui.Context) {
	th := c.Theme()
	bar := c.TitleBar()
	h := bar.Height
	if h < 36 {
		h = 36
	}
	tabH := h - 8
	if tabH < 30 {
		tabH = 30
	}
	ui.Row(c).Height(h).Padding(0, bar.Right+6, 0, bar.Left+8).Gap(6).
		AlignItems(ui.Center).Background(th.Surface).DragWindow().Children(func() {
		if src := appIcon(); src != nil {
			ui.Image(c, src).Size(18, 18).Tooltip("browser-use")
		}
		ui.ScrollBoth(c).Grow(1).Height(h).Scrollbars(false).Children(func() {
			ui.Row(c).Height(h).Gap(3).AlignItems(ui.Center).Children(func() {
				for _, info := range a.mgr.Tabs() {
					a.tabItem(c, info, tabH)
				}
			})
		})
		if a.iconButton(c, iconPlus, "New tab", false) {
			a.openTab("about:blank")
		}
	})
}

func (a *App) tabItem(c *ui.Context, info browser.TabInfo, height float32) {
	th := c.Theme()
	closed := false
	tab := ui.ButtonBase(c).Label(info.Title).Height(height).Width(190).Padding(4, 8).Gap(7).Radius(8).
		Cursor(ui.CursorPointer)
	if info.Active {
		tab.Background(th.Background).Border(1, th.Border)
	} else if tab.Hovered() {
		tab.Background(th.SurfaceHover)
	}
	tab.Children(func() {
		ui.Row(c).Size(16, 16).Radius(8).Background(tabColor(info.ID)).Center().Children(func() {
			ui.Text(c, titleInitial(info.Title)).FontSize(9).Bold().TextColor(ui.RGB(255, 255, 255))
		})
		titleColor := th.TextMuted
		if info.Active {
			titleColor = th.Text
		}
		ui.Text(c, info.Title).SingleLine().Grow(1).FontSize(12).TextColor(titleColor)
		if info.Loading {
			ui.Spinner(c).Size(12, 12)
		}
		if info.Locked {
			ui.Icon(c, iconLock).Size(12, 12).TextColor(th.Accent)
		}
		if info.Active || tab.Hovered() {
			cb := ui.ButtonBase(c).Label("Close tab").Size(18, 18).Radius(5).
				Cursor(ui.CursorPointer).Tooltip("Close tab")
			if cb.Hovered() {
				cb.Background(th.SurfacePressed)
			}
			cb.Children(func() {
				ui.Icon(c, iconClose).Size(11, 11).TextColor(th.TextMuted)
			})
			if cb.Clicked() {
				closed = true
			}
		}
		if info.Active {
			ui.Row(c).Absolute().Bottom(0).Left(10).Right(10).Height(2).Radius(1).
				Background(th.Accent)
		}
	})
	if closed {
		a.closeTab(info.ID)
		return
	}
	if tab.Clicked() {
		if err := a.mgr.Activate(info.ID); err == nil {
			a.addressAuto = true
		}
	}
}

var tabPalette = []ui.Color{
	ui.RGB(37, 99, 235),
	ui.RGB(124, 58, 237),
	ui.RGB(219, 39, 119),
	ui.RGB(234, 88, 12),
	ui.RGB(5, 150, 105),
	ui.RGB(8, 145, 178),
}

func tabColor(id string) ui.Color {
	h := 0
	for _, r := range id {
		h = (h*31 + int(r)) & 0x7fffffff
	}
	return tabPalette[h%len(tabPalette)]
}

func titleInitial(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "?"
	}
	r := []rune(s)
	return strings.ToUpper(string(r[0]))
}

// ---- toolbar -------------------------------------------------------------

func (a *App) toolbar(c *ui.Context) {
	th := c.Theme()
	active := a.mgr.Active()
	canBack, canForward, loading := false, false, false
	masked := false
	if active != nil {
		canBack = active.Page.CanGoBack()
		canForward = active.Page.CanGoForward()
		loading = active.Page.IsLoading()
		masked = a.mgr.Masked(active.ID)
	}
	ui.Row(c).Height(48).Padding(6, 10).Gap(2).AlignItems(ui.Center).
		Background(th.Background).Children(func() {
		if a.iconButton(c, iconBack, "Back", !canBack || masked) {
			a.history("back")
		}
		if a.iconButton(c, iconForward, "Forward", !canForward || masked) {
			a.history("forward")
		}
		if loading {
			if a.iconButton(c, iconStop, "Stop", masked) {
				go func() {
					_, _ = a.mgr.Navigate(context.Background(), active.ID, "ui", "stop", "", browser.NavigateOptions{WaitUntil: "none"})
				}()
			}
		} else if a.iconButton(c, iconReload, "Reload", masked) {
			a.history("reload")
		}
		a.addressBar(c, active, masked)
		a.agentChip(c, a.mgr.State())
	})
}

func (a *App) addressBar(c *ui.Context, active *browser.Tab, masked bool) {
	th := c.Theme()
	if a.addressAuto && active != nil {
		a.address = active.URL()
	}
	var input ui.Element
	bar := ui.Row(c).Grow(1).Height(32).Padding(0, 6).Gap(6).Radius(9).
		AlignItems(ui.Center).Background(th.Surface).Border(1, th.Border)
	bar.Children(func() {
		ic := iconGlobe
		if strings.HasPrefix(strings.ToLower(a.address), "https://") {
			ic = iconLock
		}
		ui.Icon(c, ic).Size(14, 14).TextColor(th.TextMuted)
		input = ui.TextInputBase(c, &a.address).Grow(1).FontSize(13).
			TextColor(th.Text).Placeholder("Enter a URL or search…").Disabled(masked)
		if input.Changed() {
			a.addressAuto = false
		}
		if input.Submitted() {
			a.navigate(a.address)
		}
		if a.iconButtonSized(c, iconArrowRight, "Go", masked, 24, 14) {
			a.navigate(a.address)
		}
	})
	if input.Focused() {
		bar.Border(1, th.Accent)
	}
}

func (a *App) agentChip(c *ui.Context, cs browser.ControlState) {
	th := c.Theme()
	label, dot := "Agent idle", th.TextMuted
	switch cs.State {
	case "agent":
		label, dot = "Agent working", th.Accent
	case "user":
		label, dot = "You have control", th.Warning
	}
	ui.Row(c).Height(26).Padding(0, 10).Gap(6).Radius(13).
		Background(th.Surface).Border(1, th.Border).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "●").FontSize(8).TextColor(dot)
		ui.Text(c, label).FontSize(11).TextColor(th.TextMuted)
	})
}

// ---- page ----------------------------------------------------------------

func (a *App) page(c *ui.Context) {
	th := c.Theme()
	active := a.mgr.Active()
	if active == nil {
		ui.Column(c).Grow(1).Center().Gap(10).Children(func() {
			ui.Icon(c, iconGlobe).Size(44, 44).TextColor(th.TextMuted)
			ui.Text(c, "No tabs open").FontSize(18).Bold()
			ui.Text(c, "Open a URL, or let an agent drive this browser.").
				FontSize(12).TextColor(th.TextMuted)
			if ui.PrimaryButton(c, "New Tab").Clicked() {
				a.openTab("about:blank")
			}
		})
		return
	}
	ui.Column(c).Grow(1).Margin(8, 10, 10, 10).Children(func() {
		ui.WebView(c, active.View).Grow(1).Radius(10).Border(1, th.Border)
	})
	masked := a.mgr.Masked(active.ID)
	if masked && !a.wasMasked {
		a.maskFocus = true
	}
	a.wasMasked = masked
	if masked {
		a.mask(c, active)
	}
	if a.showActivity {
		a.activityPanel(c)
	}
}

func (a *App) mask(c *ui.Context, tab *browser.Tab) {
	th := c.Theme()
	dim := float32(a.cfg.Control.Dim) / 255
	if dim <= 0 {
		dim = 0.08
	}
	// Absolutes are relative to the window: leave the tab strip and status
	// bar interactive (43+1 above, 1+27 below).
	overlay := ui.Column(c).Absolute().Top(chromeTop(c)).Left(0).Right(0).Bottom(28).
		Background(ui.RGBA(10, 15, 30, dim)).
		Border(1, th.Accent).Cursor(ui.CursorNotAllowed)
	if a.maskFocus {
		overlay.Focusable().Focus()
		a.maskFocus = false
	}
	overlay.Children(func() {
		ui.Row(c).Absolute().Top(14).Left(0).Right(0).Justify(ui.Center).Children(func() {
			card := ui.Row(c).Padding(9, 14).Gap(12).Radius(12).
				Background(th.Surface).Border(1, th.Border).
				Shadow(0, 8, 28, 0, ui.RGBA(0, 0, 0, 45)).AlignItems(ui.Center)
			card.Children(func() {
				ui.Row(c).Size(30, 30).Radius(15).Background(th.Accent).Center().Children(func() {
					ui.Icon(c, iconShield).Size(17, 17).TextColor(th.AccentText)
				})
				ui.Column(c).Gap(2).Children(func() {
					ui.Text(c, "Agent is controlling").FontSize(13).Bold()
					act := tab.LastAction()
					if act == "" {
						act = "getting ready"
					}
					ui.Text(c, "Page input is locked · "+clip(act, 48)).
						FontSize(11).TextColor(th.TextMuted)
				})
				if ui.PrimaryButton(c, "Take over").Clicked() {
					a.takeOver()
				}
			})
		})
		ui.Row(c).Absolute().Bottom(16).Left(0).Right(0).Justify(ui.Center).Children(func() {
			ui.Row(c).Padding(6, 12).Gap(6).Radius(14).Background(th.Surface).
				Border(1, th.Border).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, iconLock).Size(12, 12).TextColor(th.TextMuted)
				ui.Text(c, "Click Take over to interact with the page").
					FontSize(11).TextColor(th.TextMuted)
			})
		})
	})
}

// ---- status --------------------------------------------------------------

func (a *App) statusBar(c *ui.Context) {
	th := c.Theme()
	cs := a.mgr.State()
	ui.Row(c).Height(27).Padding(4, 10).Gap(10).AlignItems(ui.Center).
		Background(th.Surface).Children(func() {
		n := len(a.mgr.Tabs())
		word := "tabs"
		if n == 1 {
			word = "tab"
		}
		ui.Textf(c, "%d %s", n, word).TextColor(th.TextMuted).FontSize(11)
		ui.Text(c, "·").TextColor(th.Border).FontSize(11)
		state, col := "Agent idle", th.TextMuted
		switch cs.State {
		case "agent":
			state, col = "Agent working", th.Accent
		case "user":
			state, col = "You have control", th.Warning
		}
		ui.Text(c, state).TextColor(col).FontSize(11)
		if a.srv != nil {
			ui.Textf(c, ":%d", a.srv.Port()).TextColor(th.TextMuted).FontSize(10)
		}
		if a.lastErr != "" {
			ui.Text(c, clip(a.lastErr, 90)).TextColor(th.Danger).FontSize(11)
		}
		ui.Row(c).Grow(1)
		if ui.Button(c, "Activity").Clicked() {
			a.showActivity = !a.showActivity
		}
		if cs.State == "user" {
			if ui.Button(c, "Resume agent").Clicked() {
				a.resume()
			}
		}
	})
}

// ---- activity ------------------------------------------------------------

func (a *App) activityPanel(c *ui.Context) {
	if a.srv == nil {
		return
	}
	th := c.Theme()
	entries := a.srv.Activity()
	panel := ui.Column(c).Absolute().Top(chromeTop(c)).Right(0).Bottom(28).Width(360).
		Background(th.Background).Border(1, th.Border).
		Shadow(-10, 0, 30, 0, ui.RGBA(0, 0, 0, 35))
	panel.Children(func() {
		ui.Row(c).Height(44).Padding(0, 12).Gap(8).AlignItems(ui.Center).
			Background(th.Surface).Children(func() {
			ui.Icon(c, iconActivity).Size(16, 16).TextColor(th.Accent)
			ui.Text(c, "Activity").Bold()
			ui.Row(c).Grow(1)
			ui.Textf(c, "%d", len(entries)).FontSize(10).TextColor(th.TextMuted)
			if a.iconButton(c, iconClose, "Close", false) {
				a.showActivity = false
			}
		})
		if len(entries) == 0 {
			ui.Column(c).Grow(1).Center().Gap(8).Children(func() {
				ui.Icon(c, iconActivity).Size(28, 28).TextColor(th.TextMuted)
				ui.Text(c, "No calls yet").FontSize(12).TextColor(th.TextMuted)
			})
			return
		}
		ui.Scroll(c).Grow(1).Children(func() {
			for i := len(entries) - 1; i >= 0; i-- {
				a.activityRow(c, entries[i])
				if i > 0 {
					ui.Divider(c)
				}
			}
		})
	})
}

func (a *App) activityRow(c *ui.Context, e audit.Entry) {
	th := c.Theme()
	row := ui.Row(c).Padding(7, 12).Gap(8).AlignItems(ui.Center)
	if row.Hovered() {
		row.Background(th.SurfaceHover)
	}
	row.Children(func() {
		dot := th.Success
		if !e.OK {
			dot = th.Danger
		}
		ui.Text(c, "●").FontSize(7).TextColor(dot)
		ui.Column(c).Grow(1).Gap(1).Children(func() {
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Text(c, e.Tool).FontSize(12).Bold()
				if e.Tab != "" {
					ui.Text(c, e.Tab).FontSize(10).TextColor(th.TextMuted)
				}
				ui.Row(c).Grow(1)
			})
			detail := e.URL
			if !e.OK && e.Code != "" {
				detail = e.Code
			}
			if detail != "" {
				color := th.TextMuted
				if !e.OK {
					color = th.Danger
				}
				ui.Text(c, clip(detail, 56)).FontSize(10).TextColor(color)
			}
		})
		ui.Textf(c, "%dms", e.MS).FontSize(10).TextColor(th.TextMuted)
	})
}

// ---- helpers -------------------------------------------------------------

func (a *App) iconButton(c *ui.Context, icon *ui.SVG, tip string, disabled bool) bool {
	return a.iconButtonSized(c, icon, tip, disabled, 30, 16)
}

func (a *App) iconButtonSized(c *ui.Context, icon *ui.SVG, tip string, disabled bool, size, iconSize float32) bool {
	th := c.Theme()
	b := ui.ButtonBase(c).Label(tip).Size(size, size).Radius(7).Cursor(ui.CursorPointer).
		Tooltip(tip).Disabled(disabled)
	if disabled {
		b.Cursor(ui.CursorDefault)
	}
	if !disabled && b.Hovered() {
		b.Background(th.SurfaceHover)
	}
	color := th.Text
	if disabled {
		color = th.TextMuted
	}
	b.Children(func() {
		ui.Icon(c, icon).Size(iconSize, iconSize).TextColor(color)
	})
	if disabled {
		return false
	}
	return b.Clicked()
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
