package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"browseruse/internal/config"
)

// Client talks to the running browser-use loopback API.
type Client struct {
	base     string
	token    string
	http     *http.Client
	clientID string
}

func newClient(info *config.RuntimeInfo) *Client {
	return &Client{
		base:     info.URL(),
		token:    info.Token,
		http:     &http.Client{Timeout: 320 * time.Second},
		clientID: fmt.Sprintf("cli-%d", os.Getpid()),
	}
}

// SetClientID changes the X-Client-Id used for calls.
func (c *Client) SetClientID(id string) { c.clientID = id }

// ClientID returns the X-Client-Id used for calls.
func (c *Client) ClientID() string { return c.clientID }

// Health checks that the app is reachable.
func (c *Client) Health(ctx context.Context) error { return c.health(ctx) }

// Call invokes one tool and returns the raw /v1/call envelope.
func (c *Client) Call(ctx context.Context, name string, args any) (json.RawMessage, error) {
	return c.call(ctx, name, args)
}

func (c *Client) health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/v1/health", nil)
	if err != nil {
		return err
	}
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("health: %s", res.Status)
	}
	return nil
}

func (c *Client) call(ctx context.Context, name string, args any) (json.RawMessage, error) {
	body, err := json.Marshal(map[string]any{"name": name, "args": args})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/v1/call", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	clientID := c.clientID
	if clientID == "" {
		clientID = fmt.Sprintf("cli-%d", os.Getpid())
	}
	req.Header.Set("X-Client-Id", clientID)
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

// Ensure finds a running app, or starts one in the background.
// The auto-started instance uses --serve so CLI/MCP calls never restore or save
// the user's window session (see README "Session restore").
func Ensure(ctx context.Context) (*Client, error) {
	return EnsureServe(ctx, 0)
}

// EnsureServe starts the background HTTP instance if needed, optionally on a fixed port.
//
// 「HTTP 启动方式」就是 --serve：控制 API 可用、不动用户会话（不恢复也不保存标签页）。
// 应用已经在任何端口上跑着时直接复用它，不会起第二个窗口。
func EnsureServe(ctx context.Context, port int) (*Client, error) {
	args := []string{"--serve"}
	if port > 0 {
		args = append(args, "--port", strconv.Itoa(port))
	}
	return ensure(ctx, args)
}

func ensure(ctx context.Context, startArgs []string) (*Client, error) {
	if info, err := config.LoadRuntime(); err == nil {
		c := newClient(info)
		if err := c.health(ctx); err == nil {
			return c, nil
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(exe, startArgs...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.SysProcAttr = detach()
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start browser-use: %w", err)
	}
	_ = cmd.Process.Release()

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if info, err := config.LoadRuntime(); err == nil {
			c := newClient(info)
			if err := c.health(ctx); err == nil {
				return c, nil
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
	return nil, fmt.Errorf("browser-use did not start; run it once to see the window")
}

// startCommand 处理 `cli start [--port N]`：以 HTTP 模式启动后台实例并打印 runtime 信息。
// 应用已在运行时直接复用；插件依赖这个命令做「进程检测 + 不存在就启动」。
func startCommand(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	port := fs.Int("port", 0, "control server port (0 chooses one)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if _, err := EnsureServe(ctx, *port); err != nil {
		fmt.Fprintln(os.Stderr, "browser-use:", err)
		return 1
	}
	info, err := config.LoadRuntime()
	if err != nil {
		fmt.Fprintln(os.Stderr, "browser-use:", err)
		return 1
	}
	out, _ := json.Marshal(map[string]any{
		"ok": true, "app": info.App, "version": info.Version,
		"pid": info.PID, "port": info.Port, "url": info.URL(), "mode": "http",
	})
	fmt.Println(string(out))
	return 0
}

// Run executes the cli subcommand. It returns a process exit code.
func Run(args []string) int {
	clientID, args := clientFlag(args)
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: browser-use cli <command> [flags]")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := args[0]
	rest := args[1:]
	// 用法错误不要先拉起浏览器：缺工具名时直接返回。
	if cmd == "call" && len(rest) == 0 {
		fmt.Fprintln(os.Stderr, "browser-use: cli call needs a tool name")
		return 2
	}
	// start 不需要先 Ensure：它本身就是「不存在就启动」。
	if cmd == "start" {
		return startCommand(ctx, rest)
	}
	c, err := Ensure(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "browser-use:", err)
		return 1
	}
	if clientID != "" {
		c.SetClientID(clientID)
	}
	var name string
	var params any

	switch cmd {
	case "status":
		name = "status"
		params = map[string]any{"include_tabs": hasFlag(rest, "tabs")}

	case "tabs":
		name = "tabs"
		if len(rest) == 0 {
			params = map[string]any{"action": "list"}
			break
		}
		sub := rest[0]
		fs := flag.NewFlagSet("tabs", flag.ContinueOnError)
		tab := fs.String("tab", "", "tab id")
		force := fs.Bool("force", false, "close a user tab")
		activate := fs.Bool("activate", true, "activate a new tab")
		_ = fs.Parse(rest[1:])
		params = map[string]any{"action": sub, "tab": *tab, "force": *force, "activate": *activate}
		if fs.NArg() > 0 {
			params.(map[string]any)["url"] = fs.Arg(0)
		}

	case "navigate":
		name = "navigate"
		fs := flag.NewFlagSet("navigate", flag.ContinueOnError)
		tab := fs.String("tab", "", "tab id")
		action := fs.String("action", "goto", "goto|back|forward|reload|stop")
		wait := fs.String("wait", "load", "load|none")
		_ = fs.Parse(rest)
		u := fs.Arg(0)
		params = map[string]any{"action": *action, "tab": *tab, "url": u, "wait_until": *wait}

	case "snapshot":
		name = "snapshot"
		fs := flag.NewFlagSet("snapshot", flag.ContinueOnError)
		tab := fs.String("tab", "", "tab id")
		scope := fs.String("scope", "full", "full|viewport")
		maxChars := fs.Int("max-chars", 0, "maximum characters")
		filter := fs.String("filter", "", "filter role/name/tag")
		_ = fs.Parse(rest)
		params = map[string]any{"tab": *tab, "scope": *scope, "max_chars": *maxChars, "filter": *filter}

	case "read":
		name = "read"
		fs := flag.NewFlagSet("read", flag.ContinueOnError)
		tab := fs.String("tab", "", "tab id")
		selector := fs.String("selector", "", "element selector")
		maxChars := fs.Int("max-chars", 0, "maximum characters")
		_ = fs.Parse(rest)
		params = map[string]any{"tab": *tab, "selector": *selector, "max_chars": *maxChars}

	case "network":
		name = "network"
		action := "list"
		if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
			action = rest[0]
			rest = rest[1:]
		}
		fs := flag.NewFlagSet("network", flag.ContinueOnError)
		tab := fs.String("tab", "", "tab id")
		urlContains := fs.String("url-contains", "", "URL substring")
		method := fs.String("method", "", "HTTP method")
		status := fs.Int("status", 0, "HTTP status")
		since := fs.String("since", "", "only entries after this id")
		limit := fs.Int("limit", 50, "max entries")
		includeBody := fs.Bool("include-body", false, "include request/response bodies")
		maxBody := fs.Int("max-body", 0, "truncate bodies to this many chars")
		id := fs.String("id", "", "request id for get")
		timeout := fs.Int("timeout", 5000, "wait timeout in ms")
		if err := fs.Parse(rest); err != nil {
			return 2
		}
		params = map[string]any{
			"action": action, "tab": *tab, "url_contains": *urlContains,
			"method": *method, "status": *status, "since": *since,
			"limit": *limit, "include_body": *includeBody, "max_body": *maxBody, "id": *id,
			"timeout_ms": *timeout,
		}
	case "console":
		name = "console"
		action := "list"
		if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
			action = rest[0]
			rest = rest[1:]
		}
		fs := flag.NewFlagSet("console", flag.ContinueOnError)
		tab := fs.String("tab", "", "tab id")
		level := fs.String("level", "", "log|info|warn|error|debug")
		contains := fs.String("contains", "", "text substring")
		source := fs.String("source", "", "console|exception|rejection")
		since := fs.String("since", "", "only entries after this id")
		limit := fs.Int("limit", 50, "max entries")
		id := fs.String("id", "", "entry id for get")
		timeout := fs.Int("timeout", 5000, "wait timeout in ms")
		if err := fs.Parse(rest); err != nil {
			return 2
		}
		params = map[string]any{
			"action": action, "tab": *tab, "level": *level, "text_contains": *contains,
			"source": *source, "since": *since, "limit": *limit, "id": *id,
			"timeout_ms": *timeout,
		}
	case "frames":
		name = "frames"
		action := "list"
		if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
			action = rest[0]
			rest = rest[1:]
		}
		fs := flag.NewFlagSet("frames", flag.ContinueOnError)
		tab := fs.String("tab", "", "tab id")
		frame := fs.String("frame", "", "frame id from frames list")
		if err := fs.Parse(rest); err != nil {
			return 2
		}
		params = map[string]any{"action": action, "tab": *tab, "frame": *frame}

	case "files":
		name = "files"
		action := "upload"
		if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
			action = rest[0]
			rest = rest[1:]
		}
		fs := flag.NewFlagSet("files", flag.ContinueOnError)
		tab := fs.String("tab", "", "tab id")
		ref := fs.String("ref", "", "file input ref")
		selector := fs.String("selector", "", "file input selector")
		text := fs.String("text", "", "visible text near the input")
		paths := []string{}
		fs.Func("path", "local file (repeatable)", func(v string) error {
			paths = append(paths, v)
			return nil
		})
		if err := fs.Parse(rest); err != nil {
			return 2
		}
		if action != "upload" {
			fmt.Fprintln(os.Stderr, "browser-use: files supports upload")
			return 2
		}
		if len(paths) == 0 {
			fmt.Fprintln(os.Stderr, "browser-use: pass at least one --path")
			return 2
		}
		params = map[string]any{
			"action": action, "tab": *tab, "ref": *ref, "selector": *selector,
			"text": *text, "paths": paths,
		}

	case "cookies":
		name = "cookies"
		action := "list"
		if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
			action = rest[0]
			rest = rest[1:]
		}
		fs := flag.NewFlagSet("cookies", flag.ContinueOnError)
		url := fs.String("url", "", "URL filter")
		cname := fs.String("name", "", "cookie name")
		value := fs.String("value", "", "cookie value")
		domain := fs.String("domain", "", "cookie domain")
		path := fs.String("path", "", "cookie path")
		secure := fs.Bool("secure", false, "Secure cookie")
		httpOnly := fs.Bool("http-only", false, "HTTPOnly cookie")
		sameSite := fs.String("same-site", "", "lax|strict|none")
		expires := fs.String("expires", "", "RFC3339 or YYYY-MM-DD")
		file := fs.String("file", "", "JSON array for import")
		if err := fs.Parse(rest); err != nil {
			return 2
		}
		params = map[string]any{
			"action": action, "url": *url, "name": *cname, "value": *value,
			"domain": *domain, "path": *path, "secure": *secure,
			"http_only": *httpOnly, "same_site": *sameSite, "expires": *expires,
		}
		if action == "import" {
			if *file == "" {
				fmt.Fprintln(os.Stderr, "browser-use: cookies import needs --file")
				return 2
			}
			b, err := os.ReadFile(*file)
			if err != nil {
				fmt.Fprintln(os.Stderr, "browser-use:", err)
				return 1
			}
			var list []any
			if err := json.Unmarshal(b, &list); err != nil {
				fmt.Fprintln(os.Stderr, "browser-use: bad cookie file:", err)
				return 2
			}
			params.(map[string]any)["cookies"] = list
		}

	case "screenshot":
		name = "screenshot"
		fs := flag.NewFlagSet("screenshot", flag.ContinueOnError)
		tab := fs.String("tab", "", "tab id")
		annotate := fs.Bool("annotate", true, "draw ref labels")
		inline := fs.Bool("inline", false, "include base64 in the response")
		maxWidth := fs.Int("max-width", 1600, "maximum image width")
		out := fs.String("out", "", "copy the PNG to this path")
		_ = fs.Parse(rest)
		params = map[string]any{"tab": *tab, "annotate": *annotate, "inline": *inline, "max_width": *maxWidth}
		raw, err := c.call(ctx, name, params)
		if err != nil {
			fmt.Fprintln(os.Stderr, "browser-use:", err)
			return 1
		}
		if *out != "" {
			var env struct {
				OK   bool `json:"ok"`
				Data struct {
					Path string `json:"path"`
				} `json:"data"`
			}
			if json.Unmarshal(raw, &env) == nil && env.OK && env.Data.Path != "" {
				b, rerr := os.ReadFile(env.Data.Path)
				if rerr == nil {
					rerr = os.WriteFile(*out, b, 0o644)
				}
				if rerr != nil {
					fmt.Fprintln(os.Stderr, "browser-use: copy screenshot:", rerr)
					return 1
				}
			}
		}
		os.Stdout.Write(raw)
		if len(raw) == 0 || raw[len(raw)-1] != '\n' {
			fmt.Println()
		}
		return exitCode(raw)

	case "scroll":
		name = "scroll"
		fs := flag.NewFlagSet("scroll", flag.ContinueOnError)
		tab := fs.String("tab", "", "tab id")
		direction := fs.String("direction", "down", "up|down|left|right")
		amount := fs.Float64("amount", 600, "pixels")
		ref := fs.String("ref", "", "scroll to this ref")
		selector := fs.String("selector", "", "scroll to this selector")
		_ = fs.Parse(rest)
		params = map[string]any{"tab": *tab, "direction": *direction, "amount": *amount, "ref": *ref, "selector": *selector}

	case "hover":
		name = "hover"
		fs := flag.NewFlagSet("hover", flag.ContinueOnError)
		tab := fs.String("tab", "", "tab id")
		ref := fs.String("ref", "", "element ref")
		selector := fs.String("selector", "", "selector")
		text := fs.String("text", "", "visible text")
		_ = fs.Parse(rest)
		params = map[string]any{"tab": *tab, "ref": *ref, "selector": *selector, "text": *text}

	case "eval":
		name = "eval"
		fs := flag.NewFlagSet("eval", flag.ContinueOnError)
		tab := fs.String("tab", "", "tab id")
		expr := fs.String("expr", "", "JavaScript expression")
		_ = fs.Parse(rest)
		e := *expr
		if e == "" {
			e = fs.Arg(0)
		}
		params = map[string]any{"tab": *tab, "expression": e}

	case "downloads":
		name = "downloads"
		params = map[string]any{}

	case "dialogs":
		name = "dialogs"
		fs := flag.NewFlagSet("dialogs", flag.ContinueOnError)
		tab := fs.String("tab", "", "tab id")
		_ = fs.Parse(rest)
		params = map[string]any{"tab": *tab}

	case "click":
		name = "click"
		fs := flag.NewFlagSet("click", flag.ContinueOnError)
		tab := fs.String("tab", "", "tab id")
		ref := fs.String("ref", "", "element ref")
		selector := fs.String("selector", "", "CSS/text/role selector")
		text := fs.String("text", "", "visible text")
		force := fs.Bool("force", false, "skip actionability checks")
		noReveal := fs.Bool("no-reveal", false, "do not reveal opacity:0 hover wrappers")
		waitAfter := fs.String("wait-after", "auto", "auto|load|none")
		_ = fs.Parse(rest)
		clickParams := map[string]any{"tab": *tab, "ref": *ref, "selector": *selector, "text": *text, "force": *force, "wait_after": *waitAfter}
		if *noReveal {
			clickParams["reveal"] = false
		}
		params = clickParams

	case "type":
		name = "type"
		fs := flag.NewFlagSet("type", flag.ContinueOnError)
		tab := fs.String("tab", "", "tab id")
		ref := fs.String("ref", "", "element ref")
		selector := fs.String("selector", "", "CSS/text/role selector")
		text := fs.String("text", "", "text to type")
		submit := fs.Bool("submit", false, "press Enter")
		noClear := fs.Bool("no-clear", false, "append instead of replacing")
		slowly := fs.Bool("slowly", false, "type character by character")
		waitAfter := fs.String("wait-after", "auto", "auto|load|none")
		_ = fs.Parse(rest)
		params = map[string]any{"tab": *tab, "ref": *ref, "selector": *selector, "text": *text, "submit": *submit, "clear": !*noClear, "slowly": *slowly, "wait_after": *waitAfter}

	case "select":
		name = "select"
		fs := flag.NewFlagSet("select", flag.ContinueOnError)
		tab := fs.String("tab", "", "tab id")
		ref := fs.String("ref", "", "element ref")
		selector := fs.String("selector", "", "selector")
		values := fs.String("values", "", "comma-separated values or labels")
		_ = fs.Parse(rest)
		var list []string
		for _, v := range strings.Split(*values, ",") {
			if v = strings.TrimSpace(v); v != "" {
				list = append(list, v)
			}
		}
		params = map[string]any{"tab": *tab, "ref": *ref, "selector": *selector, "values": list}

	case "press":
		name = "press"
		fs := flag.NewFlagSet("press", flag.ContinueOnError)
		tab := fs.String("tab", "", "tab id")
		ref := fs.String("ref", "", "element ref")
		selector := fs.String("selector", "", "selector")
		_ = fs.Parse(rest)
		key := fs.Arg(0)
		params = map[string]any{"tab": *tab, "key": key, "ref": *ref, "selector": *selector}

	case "wait":
		name = "wait"
		fs := flag.NewFlagSet("wait", flag.ContinueOnError)
		tab := fs.String("tab", "", "tab id")
		forWhat := fs.String("for", "", "load|url|selector|text|time|idle")
		value := fs.String("value", "", "condition value")
		state := fs.String("state", "visible", "visible|attached|hidden")
		timeout := fs.Int("timeout", 30000, "timeout in ms")
		_ = fs.Parse(rest)
		params = map[string]any{"tab": *tab, "for": *forWhat, "value": *value, "state": *state, "timeout_ms": *timeout}

	case "control":
		name = "control"
		fs := flag.NewFlagSet("control", flag.ContinueOnError)
		tab := fs.String("tab", "", "tab id")
		_ = fs.Parse(rest)
		action := fs.Arg(0)
		if action == "" {
			action = "state"
		}
		params = map[string]any{"action": action, "tab": *tab}

	case "call":
		if len(rest) == 0 {
			fmt.Fprintln(os.Stderr, "browser-use: cli call needs a tool name")
			return 2
		}
		name = rest[0]
		fs := flag.NewFlagSet("call", flag.ContinueOnError)
		argsJSON := fs.String("args", "{}", "JSON arguments")
		_ = fs.Parse(rest[1:])
		var v any
		if err := json.Unmarshal([]byte(*argsJSON), &v); err != nil {
			fmt.Fprintln(os.Stderr, "browser-use: bad --args:", err)
			return 2
		}
		params = v

	default:
		fmt.Fprintln(os.Stderr, "browser-use: unknown command:", cmd)
		return 2
	}

	raw, err := c.call(ctx, name, params)
	if err != nil {
		fmt.Fprintln(os.Stderr, "browser-use:", err)
		return 1
	}
	os.Stdout.Write(raw)
	if len(raw) == 0 || raw[len(raw)-1] != '\n' {
		fmt.Println()
	}
	return exitCode(raw)
}

func exitCode(raw []byte) int {
	var env struct {
		OK bool `json:"ok"`
	}
	if json.Unmarshal(raw, &env) == nil && !env.OK {
		return 1
	}
	return 0
}

func hasFlag(args []string, name string) bool {
	for _, a := range args {
		if a == "--"+name || a == "-"+name {
			return true
		}
	}
	return false
}

// clientFlag 取出全局 --client <id> / --client=<id> 并从参数里移除。
//
// 它必须在子命令的 flag 解析之前处理：CLI 的 client id 决定租约与审计归属
// （默认是 cli-<pid>，调用方没法把一次调用对应回自己的线程）。
func clientFlag(args []string) (string, []string) {
	id := ""
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--client" || a == "-client":
			if i+1 < len(args) {
				id = args[i+1]
				i++
			}
		case strings.HasPrefix(a, "--client="):
			id = strings.TrimPrefix(a, "--client=")
		case strings.HasPrefix(a, "-client="):
			id = strings.TrimPrefix(a, "-client=")
		default:
			out = append(out, a)
		}
	}
	return strings.TrimSpace(id), out
}

// RunScript implements: browser-use run <file> [-e source] [--args JSON] [--client id].
func RunScript(args []string) int {
	clientID, args := clientFlag(args)
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	code := fs.String("e", "", "inline script source")
	argsJSON := fs.String("args", "{}", "JSON arguments passed as args")
	timeout := fs.Int("timeout", 120000, "timeout in ms")
	tab := fs.String("tab", "", "tab id to activate")
	keep := fs.Bool("keep-control", false, "keep the agent lease after the script")
	language := fs.String("language", "", "js or ts (auto from the file extension)")
	_ = fs.Parse(args)

	source := *code
	lang := *language
	if source == "" {
		if fs.NArg() == 0 {
			fmt.Fprintln(os.Stderr, "browser-use: pass a script file or -e source")
			return 2
		}
		path := fs.Arg(0)
		b, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, "browser-use:", err)
			return 1
		}
		source = string(b)
		if lang == "" && strings.EqualFold(filepath.Ext(path), ".ts") {
			lang = "ts"
		}
	}
	if lang == "" {
		lang = "js"
	}
	var argv any
	if err := json.Unmarshal([]byte(*argsJSON), &argv); err != nil {
		fmt.Fprintln(os.Stderr, "browser-use: bad --args:", err)
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	c, err := Ensure(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "browser-use:", err)
		return 1
	}
	if clientID != "" {
		c.SetClientID(clientID)
	}
	params := map[string]any{
		"source": source, "language": lang, "args": argv,
		"timeout_ms": *timeout, "tab": *tab,
		"release_on_finish": !*keep,
	}
	raw, err := c.call(ctx, "script", params)
	if err != nil {
		fmt.Fprintln(os.Stderr, "browser-use:", err)
		return 1
	}
	os.Stdout.Write(raw)
	if len(raw) == 0 || raw[len(raw)-1] != '\n' {
		fmt.Println()
	}
	return exitCode(raw)
}
