//go:build windows

package main

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo"

	"computeruse/internal/api"
	"computeruse/internal/engine"
	"computeruse/internal/macro"
	"computeruse/internal/mask"
	"computeruse/internal/win32"
)

// runApp starts the desktop app: the mask, the HTTP API, the tray icon and
// the shortcuts that hold or stop the agent.
func runApp(args []string) error {
	listen := defaultListen
	if v, ok := flagValue(args, "listen"); ok && v != "" {
		listen = v
	}
	token := os.Getenv("COMPUTER_USE_TOKEN")
	if v, ok := flagValue(args, "token"); ok {
		token = v
	}
	logFile := filepath.Join(dataDir(), "logs", "computer-use.log")
	if v, ok := flagValue(args, "log-file"); ok {
		logFile = v
	}
	if flagBool(args, "no-log") {
		logFile = ""
	}
	auditLog := filepath.Join(dataDir(), "logs", "actions.jsonl")
	if logFile != "" {
		if err := prepareLog(logFile); err != nil {
			fmt.Fprintln(os.Stderr, "computer-use: cannot write the log:", err)
		}
	}

	// Fail early, while the message is still a plain error on stderr.
	if err := preflight(); err != nil {
		return err
	}

	overlay := mask.New()
	overlay.SetDisabled(flagBool(args, "no-mask"))
	if flagBool(args, "show-mask-in-shots") {
		overlay.SetProtected(false)
	}
	cfg := engine.Config{
		Mask:       overlay,
		AllowInput: !flagBool(args, "read-only"),
		LogPath:    auditLog,
	}
	if flagBool(args, "fast") {
		// No pause for the eye before a click, and the shortest wait a
		// window needs after the pointer moved.
		cfg.Lead = 0
		cfg.Settle = 4 * time.Millisecond
	}
	eng, err := engine.New(cfg)
	if err != nil {
		return err
	}
	// Macros live next to the logs, and outlive a run.
	store := macro.NewStore(filepath.Join(dataDir(), "macros.json"))
	eng.SetMacroStore(store)
	srv := api.New(eng, version, token)

	var (
		serveErr    error
		serveURL    string
		startBanner = make(chan struct{})
	)
	mygo.App.OnWindowAllClosed(func() {
		// The mask windows are the app's windows: do not quit when they
		// close, as the agent may still be reading the screen.
	})
	mygo.App.WhenReady(func() {
		if err := overlay.Start(); err != nil {
			serveErr = err
			mygo.App.Quit()
			return
		}
		overlay.SetHUD("待命 · idle", false)
		serveURL, serveErr = srv.Listen(listen)
		if serveErr != nil {
			mygo.App.Quit()
			return
		}
		if !flagBool(args, "no-tray") {
			startTray(eng, serveURL)
		}
		if !flagBool(args, "no-hotkeys") {
			registerHotkeys(eng)
		}
		close(startBanner)
	})

	// Ctrl+C in the console that started the app.
	go func() {
		c := make(chan os.Signal, 1)
		signal.Notify(c, os.Interrupt)
		<-c
		mygo.App.Quit()
	}()

	// Print where to reach the app as soon as it listens.
	go func() {
		<-startBanner
		if serveErr != nil {
			return
		}
		virtual, displays := win32.Virtual()
		fmt.Printf("Computer Use %s\n", version)
		fmt.Printf("  API     %s\n", serveURL)
		if token != "" {
			fmt.Printf("  Token   required (Authorization: Bearer …)\n")
		}
		fmt.Printf("  Desktop %d×%d at %d,%d, %d display(s)\n", virtual.W, virtual.H, virtual.X, virtual.Y, len(displays))
		if overlay.Disabled() {
			fmt.Printf("  Mask    off (--no-mask)\n")
		} else {
			fmt.Printf("  Mask    on, %d windows\n", len(displays))
		}
		if !flagBool(args, "read-only") {
			fmt.Printf("  Pause   Ctrl+Alt+Shift+P   Clear Ctrl+Alt+Shift+X   Quit Ctrl+Alt+Shift+Q\n")
		}
		if logFile != "" {
			fmt.Printf("  Log     %s\n", logFile)
		}
	}()

	runErr := mygo.App.Run()
	srv.Close()
	eng.Close()
	if serveErr != nil {
		low := strings.ToLower(serveErr.Error())
		if strings.Contains(low, "only one usage") || strings.Contains(low, "address already in use") {
			return fmt.Errorf("the API address %s is taken: another Computer Use runs there (try --listen, or computer-use mcp --url)", listen)
		}
		return serveErr
	}
	return runErr
}

// preflight proves the desktop can be captured before the app opens.
func preflight() error {
	virtual, _ := win32.Virtual()
	if virtual.Empty() {
		return errors.New("no display to work with")
	}
	probe := win32.Rect{X: virtual.X, Y: virtual.Y, W: min(64, virtual.W), H: min(64, virtual.H)}
	img, err := win32.CaptureScreen(probe)
	if err != nil {
		return fmt.Errorf("cannot capture the screen: %w", err)
	}
	if img.Bounds().Empty() {
		return errors.New("the screen capture is empty")
	}
	return nil
}

// prepareLog sends the program's messages to a file as well as stderr.
func prepareLog(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	log.SetOutput(io.MultiWriter(os.Stderr, f))
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
	return nil
}

// registerHotkeys gives the user keys that work whichever app is in front.
func registerHotkeys(eng *engine.Engine) {
	type binding struct {
		chord string
		what  string
		fn    func()
	}
	bindings := []binding{
		{"Ctrl+Alt+Shift+P", "pause", func() { eng.TogglePaused() }},
		{"Ctrl+Alt+Shift+X", "clear", func() {
			if m := eng.Mask(); m != nil {
				m.Clear()
			}
		}},
		{"Ctrl+Alt+Shift+Q", "quit", func() { mygo.App.Quit() }},
	}
	for _, b := range bindings {
		if err := mygo.GlobalShortcut.Register(b.chord, b.fn); err != nil {
			log.Printf("computer-use: cannot register %s (%s): %v", b.chord, b.what, err)
			continue
		}
		log.Printf("computer-use: %s is %s", b.chord, b.what)
	}
}

// startTray puts the app's switch in the notification area: pause, hide the
// mask, clear it, and quit.
func startTray(eng *engine.Engine, apiURL string) {
	icon, err := trayIcon()
	if err != nil {
		log.Printf("computer-use: no tray icon: %v", err)
		return
	}
	var pause, show *mygo.MenuItem
	pause = &mygo.MenuItem{
		ID: "pause", Label: "暂停 Pause", Type: mygo.MenuItemCheckbox,
		Click: func(_ *mygo.MenuItem, _ *mygo.Window) {
			eng.TogglePaused()
			pause.SetChecked(eng.Paused())
			if m := eng.Mask(); m != nil {
				if eng.Paused() {
					m.SetHUD("已暂停 · paused", false)
				} else {
					m.SetHUD("待命 · idle", false)
				}
			}
		},
	}
	show = &mygo.MenuItem{
		ID: "show", Label: "显示蒙板 Show mask", Type: mygo.MenuItemCheckbox, Checked: true,
		Click: func(item *mygo.MenuItem, _ *mygo.Window) {
			if m := eng.Mask(); m != nil {
				m.SetVisible(item.IsChecked())
			}
		},
	}
	menu := mygo.NewMenu([]*mygo.MenuItem{
		{Label: "Computer Use " + version, Disabled: true},
		{Label: apiURL, Disabled: true},
		mygo.Separator(),
		pause,
		show,
		{Label: "清除标注 Clear annotations", Click: func(_ *mygo.MenuItem, _ *mygo.Window) {
			if m := eng.Mask(); m != nil {
				m.Clear()
			}
		}},
		mygo.Separator(),
		{Label: "打开状态页 Dashboard", Click: func(_ *mygo.MenuItem, _ *mygo.Window) {
			mygo.Shell.OpenExternal(apiURL)
		}},
		{Label: "退出 Quit", Click: func(_ *mygo.MenuItem, _ *mygo.Window) { mygo.App.Quit() }},
	})
	if _, err := mygo.NewTray(mygo.TrayOptions{
		ID: "computer-use", Icon: icon, ToolTip: "Computer Use 蒙板", Menu: menu,
	}); err != nil {
		log.Printf("computer-use: no tray: %v", err)
		return
	}
	// Keep the pause item true to the engine, whichever way it changed.
	go func() {
		for {
			time.Sleep(400 * time.Millisecond)
			paused := eng.Paused()
			if pause.IsChecked() != paused {
				pause.SetChecked(paused)
			}
		}
	}()
}

// trayIcon draws the app's icon: an accent ring around a crosshair.
func trayIcon() ([]byte, error) {
	const n = 32
	img := image.NewRGBA(image.Rect(0, 0, n, n))
	accent := color.RGBA{59, 130, 246, 255}
	ink := color.RGBA{255, 255, 255, 255}
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			dx, dy := float64(x)-15.5, float64(y)-15.5
			d := dx*dx + dy*dy
			switch {
			case d > 13.5*13.5 && d < 15.5*15.5:
				img.Set(x, y, accent)
			case d < 15.5*15.5:
				img.Set(x, y, color.RGBA{11, 15, 23, 235})
			}
		}
	}
	for i := 10; i <= 21; i++ {
		img.Set(i, 15, ink)
		img.Set(i, 16, ink)
		img.Set(15, i, ink)
		img.Set(16, i, ink)
	}
	img.Set(17, 17, accent)
	img.Set(18, 18, accent)
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
