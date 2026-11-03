// Command computer-use lets an AI agent use a Windows desktop, and shows
// what it does through a mask over the screen: a click-through overlay
// that dims everything but the element the agent acts on, outlines that
// element with its coordinates, and marks the point it is about to touch.
//
//	computer-use run      # the app: mask, HTTP API, tray icon
//	computer-use mcp      # the app's tools on stdin and stdout, for MCP clients
//	computer-use shot     # one screenshot, from the command line
//	computer-use demo     # the mask with sample annotations
//	computer-use doctor   # check that the desktop can be captured and driven
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"computeruse/internal/win32"
)

// version is the release of this program.
const version = "0.1.0"

// defaultListen is where the app serves its API.
const defaultListen = "127.0.0.1:7331"

func main() {
	// Physical pixels everywhere, also when no window is created (shot).
	win32.InitProcess()

	cmd := "run"
	args := os.Args[1:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	var err error
	switch cmd {
	case "run", "app", "serve":
		err = runApp(args)
	case "mcp":
		err = runMCP(args)
	case "shot", "screenshot":
		err = runShot(args)
	case "doctor":
		err = runDoctor(args)
	case "demo":
		err = runDemo(args)
	case "script", "js":
		err = runScriptCmd(args)
	case "macro", "macros":
		err = runMacroCmd(args)
	case "version", "--version", "-v":
		fmt.Printf("computer-use %s\n", version)
	case "help", "--help", "-h":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "computer-use: unknown command %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "computer-use:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `computer-use `+version+` — 让 AI 代理操作电脑，并用蒙板标出它看到与点击的元素

用法:
  computer-use [run] [flags]    启动程序：蒙板、HTTP API、托盘图标
  computer-use mcp [flags]      MCP 服务器（stdin/stdout 上的 JSON-RPC）
  computer-use script FILE.js   在运行中的程序里执行 JS（cu.click/type/mask/record...）
  computer-use macro ...        列出、查看、导出、回放、删除宏；macro record start/stop
  computer-use shot [flags]     截取一张截图并保存
  computer-use demo [flags]     演示蒙板（示例标注）
  computer-use doctor           检查截图与注入输入是否可用
  computer-use version          显示版本

run 的参数:
  --listen ADDR      API 地址（默认 `+defaultListen+`）
  --token TOKEN      API 令牌；也可用环境变量 COMPUTER_USE_TOKEN
  --no-mask          不创建蒙板窗口
  --no-tray          不创建托盘图标
  --no-hotkeys       不注册全局快捷键
  --read-only        只读：不注入鼠标键盘
  --fast             快：动作前不给眼睛留停顿（蒙板仍显示，只是不等）
  --show-mask-in-shots  让截图包含蒙板（调试用）
  --log-file PATH    日志文件（默认在 %LOCALAPPDATA%\ComputerUse\logs）
  --no-log           不写日志文件

mcp 的参数:
  --url URL          API 地址（默认 http://`+defaultListen+`）
  --token TOKEN      API 令牌
  --spawn            未运行则启动 computer-use run

shot 的参数:
  --out PATH         输出文件（默认 screenshot.png）
  --display N        只截某个显示器（从 0 开始）
  --x --y --w --h    截取一块区域（物理像素）
  --max-width N      缩放到这个宽度（默认 1600，0 表示不缩放）
  --scale F          按比例缩放（0 到 1）
  --grid N           每 N 像素画坐标网格
  --format png|jpeg  图片格式
  --quality N        JPEG 质量（1-100）
  --cursor           在截图里标出指针与坐标
  --json             把坐标映射以 JSON 打印出来

script 的参数:
  --record NAME      把脚本执行的动作录成宏
  --timeout MS       超时（默认 60000）
  --arg k=v          传给脚本的 args.k（可重复）
  --json             打印完整结果 JSON
  --url URL          API 地址（默认 http://`+defaultListen+`）

macro 的子命令:
  list | show <name> | script <name> | run <name> [--speed N] | rm <name>
  record start [name] | record stop

demo 的参数:
  --seconds N        演示多久（默认 20）
  --grid N           坐标网格间隔（默认 100）
  --preview PATH     把蒙板预览另存为 PNG

快捷键（run）:
  Ctrl+Alt+Shift+P   暂停 / 继续
  Ctrl+Alt+Shift+X   清除标注
  Ctrl+Alt+Shift+Q   退出
`)
}

// dataDir is where the program keeps its logs.
func dataDir() string {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, "ComputerUse")
}

// flagValue returns the value of --name in args, and whether it was given.
func flagValue(args []string, name string) (string, bool) {
	for i, a := range args {
		if a == "--"+name || a == "-"+name {
			if i+1 < len(args) {
				return args[i+1], true
			}
			return "", true
		}
		if strings.HasPrefix(a, "--"+name+"=") {
			return strings.TrimPrefix(a, "--"+name+"="), true
		}
	}
	return "", false
}

// flagBool reports whether --name was given.
func flagBool(args []string, name string) bool {
	for _, a := range args {
		if a == "--"+name {
			return true
		}
	}
	return false
}
