// Command genproto 从 spec/proto.json 生成 internal/proto/methods_gen.go。
//
// spec/proto.json 是协议唯一手写源（设计 8.2）；生成的常量与 dispatch 覆盖表不允许手工编辑。
// CI 用 `go generate ./... && git diff --exit-code` 保证生成物与源同步。
//
// 用法：
//
//	go run github.com/adag/ada-go/tools/genproto            # 自动定位仓库根
//	go run github.com/adag/ada-go/tools/genproto -check     # 只校验，不写文件（diff 检查用）
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"strings"
)

// specPath 与 outPath 是相对仓库根的位置。
const (
	defaultSpec = "spec/proto.json"
	defaultOut  = "internal/proto/methods_gen.go"
	pkgName     = "proto"
)

func main() {
	var (
		specFlag = flag.String("spec", "", "协议源文件（默认自动定位 spec/proto.json）")
		outFlag  = flag.String("out", "", "生成文件（默认 internal/proto/methods_gen.go）")
		check    = flag.Bool("check", false, "只校验并比较，不写盘；内容有差异时以退出码 1 失败")
		rootFlag = flag.String("root", "", "仓库根（默认从当前目录向上找 go.mod）")
	)
	flag.Parse()

	root, err := repoRoot(*rootFlag)
	if err != nil {
		fatal(err)
	}
	specPath := resolve(root, *specFlag, defaultSpec)
	outPath := resolve(root, *outFlag, defaultOut)

	doc, err := loadSpec(specPath)
	if err != nil {
		fatal(err)
	}
	if err := doc.Validate(); err != nil {
		fatal(fmt.Errorf("%s: %w", specPath, err))
	}

	src, err := render(doc)
	if err != nil {
		fatal(err)
	}
	formatted, err := format.Source(src)
	if err != nil {
		fatal(fmt.Errorf("生成的代码不是合法 Go: %w", err))
	}

	if *check {
		old, err := os.ReadFile(outPath)
		switch {
		case os.IsNotExist(err):
			fatal(fmt.Errorf("缺少生成物 %s；运行 go generate ./...", rel(root, outPath)))
		case err != nil:
			fatal(err)
		case !bytes.Equal(old, formatted):
			fatal(fmt.Errorf("%s 与 %s 不同步；运行 go generate ./...", rel(root, outPath), rel(root, specPath)))
		}
		fmt.Printf("genproto: %s 与生成物一致（%d 方法 / %d 事件 / %d 错误码）\n",
			rel(root, specPath), len(doc.Methods), len(doc.Events), len(doc.ErrorCodes))
		return
	}

	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		fatal(err)
	}
	if err := os.WriteFile(outPath, formatted, 0o644); err != nil {
		fatal(err)
	}
	fmt.Printf("genproto: 已生成 %s（%d 方法 / %d 事件 / %d 错误码）\n",
		rel(root, outPath), len(doc.Methods), len(doc.Events), len(doc.ErrorCodes))
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "genproto:", err)
	os.Exit(1)
}

func resolve(root, flagVal, def string) string {
	if flagVal != "" {
		if filepath.IsAbs(flagVal) {
			return flagVal
		}
		return filepath.Join(root, filepath.FromSlash(flagVal))
	}
	return filepath.Join(root, filepath.FromSlash(def))
}

func repoRoot(flagVal string) (string, error) {
	if flagVal != "" {
		return filepath.Abs(flagVal)
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("找不到 go.mod：请在 ada-go 仓库内运行，或传 -root")
		}
		dir = parent
	}
}

func rel(root, p string) string {
	if r, err := filepath.Rel(root, p); err == nil {
		return filepath.ToSlash(r)
	}
	return p
}

// ---- 协议源的结构（与 spec/proto.json 一一对应）----

type document struct {
	SchemaVersion   int             `json:"schemaVersion"`
	ProtoVersion    int             `json:"protoVersion"`
	APIVersion      string          `json:"apiVersion"`
	Generated       generatedMeta   `json:"generated"`
	Transport       transport       `json:"transport"`
	Conventions     conventions     `json:"conventions"`
	JSONRPCReserved []int           `json:"jsonRpcReserved"`
	Definitions     json.RawMessage `json:"definitions"`
	ErrorCodes      []errorCode     `json:"errorCodes"`
	Methods         []method        `json:"methods"`
	Events          []event         `json:"events"`
}

type generatedMeta struct {
	GoPackage string `json:"goPackage"`
	ConstFile string `json:"constFile"`
}

type transport struct {
	Encoding                   string `json:"encoding"`
	MaxMessageBytes            int    `json:"maxMessageBytes"`
	PingIntervalMS             int    `json:"pingIntervalMs"`
	PongTimeoutMS              int    `json:"pongTimeoutMs"`
	HandshakeTimeoutMS         int    `json:"handshakeTimeoutMs"`
	EventBuffer                int    `json:"eventBuffer"`
	SnapshotFieldTruncateBytes int    `json:"snapshotFieldTruncateBytes"`
	DeltaMergeWindowMS         int    `json:"deltaMergeWindowMs"`
	DeltaMaxLatencyMS          int    `json:"deltaMaxLatencyMs"`
}

type conventions struct {
	ThreadIDFormat  string   `json:"threadIdFormat"`
	EntryIDFormat   string   `json:"entryIdFormat"`
	ItemKeyPrefixes []string `json:"itemKeyPrefixes"`
	EventIDPrefix   string   `json:"eventIdPrefix"`
	TimestampFormat string   `json:"timestampFormat"`
	Notes           []string `json:"notes"`
	JSONRPCReserved []int    `json:"jsonRpcReserved"`
}

type errorCode struct {
	Code           int      `json:"code"`
	Name           string   `json:"name"`
	Title          string   `json:"title"`
	Retryable      bool     `json:"retryable"`
	Typical        []string `json:"typical"`
	DiagnosticArea string   `json:"diagnosticArea"`
}

type method struct {
	Name                  string          `json:"name"`
	Group                 string          `json:"group"`
	Summary               string          `json:"summary"`
	Direction             string          `json:"direction"`
	Since                 string          `json:"since"`
	Idempotent            bool            `json:"idempotent"`
	RequiresReadyInstance bool            `json:"requiresReadyInstance"`
	DefaultDisabled       bool            `json:"defaultDisabled"`
	Params                json.RawMessage `json:"params"`
	Result                json.RawMessage `json:"result"`
	Errors                []string        `json:"errors"`
}

type event struct {
	Name               string          `json:"name"`
	Scope              string          `json:"scope"`
	Summary            string          `json:"summary"`
	Payload            json.RawMessage `json:"payload"`
	Replayable         bool            `json:"replayable"`
	EntersModelContext bool            `json:"entersModelContext"`
}

// reservedCodes 优先读顶层 jsonRpcReserved，兼容放在 conventions 里的写法。
func (d *document) reservedCodes() []int {
	if len(d.JSONRPCReserved) > 0 {
		return d.JSONRPCReserved
	}
	return d.Conventions.JSONRPCReserved
}

func loadSpec(path string) (*document, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取协议源失败 %s: %w", path, err)
	}
	var doc document
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("解析协议源失败 %s: %w", path, err)
	}
	return &doc, nil
}

// Validate 做结构性校验：命名、分组、引用与唯一性。数量与文档的一致性由 adag verify 负责。
func (d *document) Validate() error {
	if d.ProtoVersion <= 0 {
		return fmt.Errorf("protoVersion 必须 > 0")
	}
	if d.SchemaVersion <= 0 {
		return fmt.Errorf("schemaVersion 必须 > 0")
	}
	if strings.TrimSpace(d.APIVersion) == "" {
		return fmt.Errorf("apiVersion 不能为空")
	}
	if len(d.Methods) == 0 {
		return fmt.Errorf("methods 为空")
	}
	if len(d.Events) == 0 {
		return fmt.Errorf("events 为空")
	}
	if len(d.ErrorCodes) == 0 {
		return fmt.Errorf("errorCodes 为空")
	}
	if d.Generated.GoPackage != "internal/proto" || d.Generated.ConstFile != "methods_gen.go" {
		return fmt.Errorf("generated 指向了意外位置：%+v（应为 internal/proto/methods_gen.go）", d.Generated)
	}
	if len(d.reservedCodes()) == 0 {
		return fmt.Errorf("缺少 jsonRpcReserved：传输层保留码必须显式声明，避免与应用码混淆（8.5）")
	}
	if len(d.Definitions) == 0 {
		return fmt.Errorf("缺少 definitions：公共结构必须集中定义供 $ref 使用")
	}
	if d.Transport.MaxMessageBytes <= 0 || d.Transport.PingIntervalMS <= 0 {
		return fmt.Errorf("transport 的 maxMessageBytes / pingIntervalMs 必须 > 0")
	}

	errNames := map[string]bool{}
	codes := map[int]bool{}
	for _, e := range d.ErrorCodes {
		if e.Code <= 0 {
			return fmt.Errorf("错误码 %q 的 code 必须为正整数", e.Name)
		}
		if codes[e.Code] {
			return fmt.Errorf("错误码 %d 重复", e.Code)
		}
		codes[e.Code] = true
		if errNames[e.Name] {
			return fmt.Errorf("错误码名称 %q 重复", e.Name)
		}
		errNames[e.Name] = true
		if !isLowerSnake(e.Name) {
			return fmt.Errorf("错误码名称 %q 必须是 lower_snake", e.Name)
		}
		if strings.TrimSpace(e.DiagnosticArea) == "" {
			return fmt.Errorf("错误码 %q 缺少 diagnosticArea", e.Name)
		}
		if strings.TrimSpace(e.Title) == "" {
			return fmt.Errorf("错误码 %q 缺少 title", e.Name)
		}
	}

	seen := map[string]bool{}
	groups := map[string]int{}
	for _, m := range d.Methods {
		if seen[m.Name] {
			return fmt.Errorf("方法 %q 重复", m.Name)
		}
		seen[m.Name] = true
		if !strings.Contains(m.Name, ".") {
			return fmt.Errorf("方法 %q 必须是 group.name 形式", m.Name)
		}
		g, _, _ := strings.Cut(m.Name, ".")
		if m.Group == "" {
			return fmt.Errorf("方法 %q 缺少 group", m.Name)
		}
		if g != m.Group {
			return fmt.Errorf("方法 %q 的分组前缀 %q 与 group 字段 %q 不一致", m.Name, g, m.Group)
		}
		groups[m.Group]++
		if strings.TrimSpace(m.Summary) == "" {
			return fmt.Errorf("方法 %q 缺少 summary", m.Name)
		}
		if m.Direction == "" {
			return fmt.Errorf("方法 %q 缺少 direction", m.Name)
		}
		if len(m.Params) == 0 || string(m.Params) == "null" {
			return fmt.Errorf("方法 %q 缺少 params schema", m.Name)
		}
		if len(m.Result) == 0 || string(m.Result) == "null" {
			return fmt.Errorf("方法 %q 缺少 result schema", m.Name)
		}
		for _, e := range m.Errors {
			if errNames[e] || transportLevelErrors[e] {
				continue
			}
			return fmt.Errorf("方法 %q 引用了未知错误 %q", m.Name, e)
		}
	}

	evSeen := map[string]bool{}
	for _, ev := range d.Events {
		if evSeen[ev.Name] {
			return fmt.Errorf("事件 %q 重复", ev.Name)
		}
		evSeen[ev.Name] = true
		if !strings.HasPrefix(ev.Name, "evt.") {
			return fmt.Errorf("事件 %q 必须以 evt. 为前缀", ev.Name)
		}
		if ev.Scope != "thread" && ev.Scope != "workspace" {
			return fmt.Errorf("事件 %q 的 scope %q 必须是 thread 或 workspace", ev.Name, ev.Scope)
		}
		if strings.TrimSpace(ev.Summary) == "" {
			return fmt.Errorf("事件 %q 缺少 summary", ev.Name)
		}
		if len(ev.Payload) == 0 || string(ev.Payload) == "null" {
			return fmt.Errorf("事件 %q 缺少 payload schema", ev.Name)
		}
	}

	// $ref 只能指向 definitions 里存在的键。
	var refs []string
	collectRefs(d.Definitions, &refs)
	defKeys := map[string]bool{}
	if len(d.Definitions) > 0 {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(d.Definitions, &obj); err != nil {
			return fmt.Errorf("definitions 必须是对象: %w", err)
		}
		for k := range obj {
			defKeys[k] = true
		}
	}
	for _, s := range d.allSchemas() {
		var r []string
		collectRefs(s, &r)
		refs = append(refs, r...)
	}
	for _, r := range refs {
		name := strings.TrimPrefix(r, "#/definitions/")
		if name == r {
			continue // 非本地引用：M0 只允许 #/definitions/*
		}
		if !defKeys[name] {
			return fmt.Errorf("$ref 指向不存在的 definition: %q", r)
		}
	}
	return nil
}

// transportLevelErrors 是 JSON-RPC 层允许被方法引用的三个名字（8.5）。
var transportLevelErrors = map[string]bool{
	"invalid_params":   true,
	"method_not_found": true,
	"internal_error":   true,
}

func (d *document) allSchemas() []json.RawMessage {
	out := []json.RawMessage{d.Definitions}
	for _, m := range d.Methods {
		out = append(out, m.Params, m.Result)
	}
	for _, e := range d.Events {
		out = append(out, e.Payload)
	}
	return out
}

// collectRefs 递归收集所有 "$ref" 字符串值。
func collectRefs(raw json.RawMessage, into *[]string) {
	if len(raw) == 0 {
		return
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return
	}
	walk(v, func(key string, val any) {
		if key == "$ref" {
			if s, ok := val.(string); ok {
				*into = append(*into, s)
			}
		}
	})
}

func walk(v any, fn func(key string, val any)) {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			fn(k, val)
			walk(val, fn)
		}
	case []any:
		for _, val := range t {
			walk(val, fn)
		}
	}
}

func isLowerSnake(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
			if i == 0 {
				return false
			}
		case r == '_':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}
