package app

import (
	"context"
	"log"
	"path/filepath"
	"strings"
	"time"

	"browseruse/internal/audit"
	"browseruse/internal/browser"
	"browseruse/internal/config"
	"browseruse/internal/control"
	"browseruse/internal/version"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// App is the browser-use desktop application.
type App struct {
	cfg config.Config
	win *mygo.Window
	mgr *browser.Manager
	srv *control.Server
	log *audit.Log

	address      string
	addressAuto  bool
	maskFocus    bool
	wasMasked    bool
	lastTitle    string
	lastErr      string
	showActivity bool

	tray         *mygo.Tray
	restore      *sessionState
	sessionReady bool
	sessionSave  bool
	saveTimer    *time.Timer
}

// Run starts the GUI and its control server. initialURL is opened in the
// first tab; empty means the configured default.
func Run(cfg config.Config, initialURL string) error {
	mygo.App.SetName(config.AppName)
	mygo.App.SetVersion(version.Version)

	if !mygo.App.RequestSingleInstanceLock() {
		return nil
	}

	var restore *sessionState
	if cfg.Session.Restore && initialURL == "" {
		restore = loadSession()
	}
	a := &App{cfg: cfg, addressAuto: true, restore: restore, sessionSave: cfg.Session.Save}
	mygo.App.OnSecondInstance(func(args []string, wd string) {
		if a.mgr == nil {
			return
		}
		for _, arg := range args {
			if strings.HasPrefix(arg, "http://") || strings.HasPrefix(arg, "https://") {
				_, _ = a.mgr.NewTab(context.Background(), arg, browser.TabOptions{Owner: browser.OwnerUser, Activate: true})
			}
		}
		if a.win != nil {
			a.win.Show()
			a.win.Focus()
		}
	})

	mygo.App.WhenReady(func() {
		sess := mygo.NewSession("agent")
		width, height, x, y := cfg.UI.Width, cfg.UI.Height, 0, 0
		if a.restore != nil {
			if a.restore.Width >= 860 {
				width = a.restore.Width
			}
			if a.restore.Height >= 560 {
				height = a.restore.Height
			}
			x, y = a.restore.X, a.restore.Y
		}
		a.win = mygo.NewWindow(mygo.WindowOptions{
			Title:           "browser-use",
			Width:           width,
			Height:          height,
			X:               x,
			Y:               y,
			MinWidth:        860,
			MinHeight:       560,
			BackgroundColor: "light-dark(#f6f7f9, #111318)",
			TitleBarStyle:   mygo.TitleBarHidden,
			TitleBarHeight:  44,
			Content:         ui.View(a.view),
		})
		applyIcon(a.win)
		a.tray = startTray(a)
		a.mgr = browser.New(a.win, cfg, sess)
		a.mgr.SetOnChange(a.onChange)
		if a.addressAuto {
			a.address = cfg.Agent.DefaultURL
		}
		opened := false
		if initialURL != "" {
			_, err := a.mgr.NewTab(context.Background(), initialURL, browser.TabOptions{Owner: browser.OwnerUser, Activate: true})
			opened = err == nil
			if err != nil {
				a.lastErr = err.Error()
				log.Printf("browser-use: first tab: %v", err)
			}
		} else if a.restore != nil {
			opened = a.restoreTabs(a.restore)
		}
		if !opened {
			if _, err := a.mgr.NewTab(context.Background(), cfg.Agent.DefaultURL, browser.TabOptions{Owner: browser.OwnerUser, Activate: true}); err != nil {
				a.lastErr = err.Error()
				log.Printf("browser-use: first tab: %v", err)
			}
		}
		if a.restore != nil && a.restore.Maximized {
			a.win.Maximize()
		}
		a.restore = nil
		a.sessionReady = a.sessionSave
		if dir, derr := config.Dir(); derr == nil {
			if lg, lerr := audit.New(filepath.Join(dir, "audit")); lerr == nil {
				a.log = lg
			} else {
				log.Printf("browser-use: audit: %v", lerr)
			}
		}
		srv, err := control.NewServer(a.mgr, cfg, a.log)
		if err != nil {
			a.lastErr = "server: " + err.Error()
			log.Printf("browser-use: server: %v", err)
		} else {
			a.srv = srv
			if err := srv.Start(); err != nil {
				a.lastErr = "server: " + err.Error()
				log.Printf("browser-use: server: %v", err)
			}
		}
		a.win.Invalidate()
	})

	// 托盘图标要在退出前销毁（loop 还在跑，能处理 onMain）。
	mygo.App.OnWillQuit(func(*mygo.QuitEvent) {
		if a.tray != nil {
			a.tray.Destroy()
			a.tray = nil
		}
	})
	mygo.App.OnQuit(func() {
		if a.saveTimer != nil {
			a.saveTimer.Stop()
			a.saveTimer = nil
		}
		a.saveSessionNow()
		if a.mgr != nil {
			a.mgr.Close()
		}
		if a.srv != nil {
			a.srv.Close()
		}
		if a.log != nil {
			a.log.Close()
		}
	})
	return mygo.App.Run()
}

func (a *App) onChange() {
	mygo.RunOnMain(func() {
		if a.win == nil || a.mgr == nil {
			return
		}
		if a.addressAuto {
			if t := a.mgr.Active(); t != nil {
				a.address = t.URL()
			}
		}
		if t := a.mgr.Active(); t != nil {
			title := t.Title() + " — browser-use"
			if title != a.lastTitle {
				a.lastTitle = title
				a.win.SetTitle(title)
			}
		}
		a.scheduleSessionSave()
		a.win.Invalidate()
	})
}

func (a *App) takeOver() {
	a.mgr.TakeOver()
	a.maskFocus = false
	if t := a.mgr.Active(); t != nil && t.View != nil {
		t.View.Focus()
	}
	a.win.Invalidate()
}

func (a *App) pause() {
	a.mgr.Pause()
	a.maskFocus = false
	if t := a.mgr.Active(); t != nil && t.View != nil {
		t.View.Focus()
	}
	a.win.Invalidate()
}

func (a *App) resume() {
	a.mgr.Resume()
	a.win.Invalidate()
}

func (a *App) openTab(rawURL string) {
	if strings.TrimSpace(rawURL) == "" {
		rawURL = a.cfg.Agent.DefaultURL
	}
	go func() {
		_, err := a.mgr.NewTab(context.Background(), rawURL, browser.TabOptions{Owner: browser.OwnerUser, Activate: true})
		mygo.RunOnMain(func() {
			if err != nil {
				a.lastErr = err.Error()
			}
			a.addressAuto = true
			a.win.Invalidate()
		})
	}()
}

func (a *App) closeTab(id string) {
	go func() {
		err := a.mgr.CloseTab(id, true)
		mygo.RunOnMain(func() {
			if err != nil {
				a.lastErr = err.Error()
			}
			a.addressAuto = true
			a.win.Invalidate()
		})
	}()
}

func (a *App) navigate(rawURL string) {
	go func() {
		_, err := a.mgr.Navigate(context.Background(), a.mgr.ActiveID(), "ui", "goto", rawURL, browser.NavigateOptions{WaitUntil: "load", Timeout: 30 * time.Second, Human: true})
		mygo.RunOnMain(func() {
			if err != nil {
				a.lastErr = err.Error()
			}
			a.addressAuto = true
			a.win.Invalidate()
		})
	}()
}

func (a *App) history(action string) {
	go func() {
		_, err := a.mgr.Navigate(context.Background(), a.mgr.ActiveID(), "ui", action, "", browser.NavigateOptions{WaitUntil: "load", Timeout: 30 * time.Second, Human: true})
		mygo.RunOnMain(func() {
			if err != nil {
				a.lastErr = err.Error()
			}
			a.win.Invalidate()
		})
	}()
}
