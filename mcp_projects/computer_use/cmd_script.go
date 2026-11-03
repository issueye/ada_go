//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// valueFlags are the flags that take a value, which keeps their values from
// being read as file names or macro names.
var valueFlags = map[string]bool{
	"--record": true, "--timeout": true, "--arg": true,
	"--url": true, "--token": true, "--speed": true, "--name": true,
}

// positional returns the arguments that are not flags or flag values.
func positional(args []string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			name, _, hasValue := strings.Cut(a, "=")
			if !hasValue && valueFlags[name] && i+1 < len(args) {
				i++
			}
			continue
		}
		out = append(out, a)
	}
	return out
}

// runScriptCmd runs a JavaScript file against the running app:
//
//	computer-use script demo.js [--record NAME] [--timeout MS] [--arg k=v]
func runScriptCmd(args []string) error {
	pos := positional(args)
	if len(pos) == 0 {
		return fmt.Errorf("usage: computer-use script <file.js> [--record NAME] [--timeout MS] [--arg k=v]")
	}
	path := pos[0]
	src, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	client := newClient(args)
	req := map[string]any{"source": string(src), "name": filepath.Base(path)}
	if v, ok := flagValue(args, "record"); ok && v != "" {
		req["record"] = v
	}
	if v, ok := flagValue(args, "timeout"); ok && v != "" {
		req["timeout_ms"] = atoi(v)
	}
	if kv := flagArgs(args, "arg"); len(kv) > 0 {
		req["args"] = kv
	}
	var res struct {
		OK         bool     `json:"ok"`
		Result     any      `json:"result"`
		Output     []string `json:"output"`
		Error      string   `json:"error"`
		DurationMS int64    `json:"duration_ms"`
		Macro      string   `json:"macro"`
	}
	if err := client.do("POST", "/v1/script", req, &res); err != nil {
		return err
	}
	for _, line := range res.Output {
		fmt.Println(line)
	}
	if res.Macro != "" {
		fmt.Printf("· 宏已保存 · macro saved: %s\n", res.Macro)
	}
	if flagBool(args, "json") {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(res)
	} else if res.Result != nil {
		raw, _ := json.Marshal(res.Result)
		fmt.Printf("→ %s\n", raw)
	}
	if !res.OK {
		if res.Error != "" {
			fmt.Fprintln(os.Stderr, res.Error)
		}
		return fmt.Errorf("the script failed after %d ms", res.DurationMS)
	}
	fmt.Fprintf(os.Stderr, "· %d ms\n", res.DurationMS)
	return nil
}

// runMacroCmd lists, shows, runs and deletes macros:
//
//	computer-use macro list|show|script|run|rm [name] [--speed N]
//	computer-use macro record start [name] | stop
func runMacroCmd(args []string) error {
	pos := positional(args)
	cmd := ""
	if len(pos) > 0 {
		cmd = pos[0]
	}
	name := ""
	if len(pos) > 1 {
		name = pos[1]
	}
	client := newClient(args)
	switch cmd {
	case "list", "ls", "":
		var res struct {
			Macros []struct {
				Name    string `json:"name"`
				Steps   int    `json:"steps"`
				Updated string `json:"updated"`
				Source  string `json:"source"`
			} `json:"macros"`
		}
		if err := client.do("GET", "/v1/macros", nil, &res); err != nil {
			return err
		}
		if len(res.Macros) == 0 {
			fmt.Println("还没有宏 · no macros yet")
			return nil
		}
		for _, m := range res.Macros {
			fmt.Printf("%-30s %3d 步  %-10s %s\n", m.Name, m.Steps, m.Source, m.Updated)
		}
		return nil
	case "show":
		if name == "" {
			return fmt.Errorf("usage: computer-use macro show <name>")
		}
		raw, err := client.raw("GET", "/v1/macros/"+name, nil)
		if err != nil {
			return err
		}
		var pretty any
		if json.Unmarshal(raw, &pretty) == nil {
			out, _ := json.MarshalIndent(pretty, "", "  ")
			fmt.Println(string(out))
			return nil
		}
		fmt.Println(string(raw))
		return nil
	case "script":
		if name == "" {
			return fmt.Errorf("usage: computer-use macro script <name>")
		}
		raw, err := client.raw("GET", "/v1/macros/"+name+"/script", nil)
		if err != nil {
			return err
		}
		fmt.Print(string(raw))
		return nil
	case "run":
		if name == "" {
			return fmt.Errorf("usage: computer-use macro run <name> [--speed N]")
		}
		body := map[string]any{}
		if v, ok := flagValue(args, "speed"); ok && v != "" {
			var f float64
			fmt.Sscanf(v, "%f", &f)
			body["speed"] = f
		}
		var res struct {
			Steps int `json:"steps"`
		}
		if err := client.do("POST", "/v1/macros/"+name+"/run", body, &res); err != nil {
			return err
		}
		fmt.Printf("宏 %s 回放完成：%d 步\n", name, res.Steps)
		return nil
	case "rm", "delete":
		if name == "" {
			return fmt.Errorf("usage: computer-use macro rm <name>")
		}
		if err := client.do("DELETE", "/v1/macros/"+name, nil, nil); err != nil {
			return err
		}
		fmt.Printf("宏 %s 已删除\n", name)
		return nil
	case "record":
		sub := ""
		if len(pos) > 1 {
			sub = pos[1]
		}
		recName := ""
		if len(pos) > 2 {
			recName = pos[2]
		}
		switch sub {
		case "start":
			var res struct {
				Name string `json:"name"`
			}
			if err := client.do("POST", "/v1/record/start", map[string]any{"name": recName}, &res); err != nil {
				return err
			}
			fmt.Printf("开始录制：%s（现在用 API、MCP 或脚本操作的每一步都会记下来）\n", res.Name)
			return nil
		case "stop":
			var res struct {
				Macro struct {
					Name  string `json:"name"`
					Steps []any  `json:"steps"`
				} `json:"macro"`
			}
			if err := client.do("POST", "/v1/record/stop", nil, &res); err != nil {
				return err
			}
			fmt.Printf("录制完成：%s（%d 步）\n", res.Macro.Name, len(res.Macro.Steps))
			return nil
		}
		return fmt.Errorf("usage: computer-use macro record start [name] | stop")
	}
	return fmt.Errorf("unknown macro command %q (list, show, script, run, rm, record)", cmd)
}

// flagArgs collects the values of a repeated flag, as --arg k=v.
func flagArgs(args []string, name string) map[string]string {
	out := map[string]string{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--"+name || a == "-"+name {
			if i+1 < len(args) {
				addArg(out, args[i+1])
				i++
			}
			continue
		}
		if strings.HasPrefix(a, "--"+name+"=") {
			addArg(out, strings.TrimPrefix(a, "--"+name+"="))
		}
	}
	return out
}

func addArg(out map[string]string, kv string) {
	if k, v, ok := strings.Cut(kv, "="); ok {
		out[k] = v
	}
}
