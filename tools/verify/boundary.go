package verify

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// officialPluginIDs 是设计 6.8 表里列出的官方插件 id。
//
// 这份清单只允许出现在门禁工具（tools/verify）与 products/*.agent.json 里：
// 基座源码（internal/、app/、cmd/）出现任意一个都算边界漂移（1.5.5「零插件 id」）。
var officialPluginIDs = []string{
	"provider-openai-chat", "provider-anthropic-messages", "provider-openai-responses",
	"tools-fs", "tools-shell", "tools-git", "tools-plan", "tools-ask", "tools-skill", "tools-subagent",
	"policy-default", "persona-coding", "provider-admin", "token-usage",
	"ui-hello-tsx",
}

// goFileRe 匹配可能构成「平行 Go 注册入口」的导出函数（1.5.2「零协议扩展」「基座不提供 Go 插件注册 API」）。
var registrationAPIRe = regexp.MustCompile(`(?m)^func\s+(?:\([^)]+\)\s+)?(RegisterTool|RegisterProvider|RegisterPrompt|RegisterAction|RegisterRoute|RegisterSlot|RegisterHook|AddTool|BuiltinTool)\s*\(`)

// importOfficialRe 匹配对官方插件源码包（plugins/official）的 import，
// 同时兼容模块根包旧路径 official。组合根 app/ 是唯一允许 import 它的包
// （01 §3.3：app 唯一知道官方插件包）；internal/ 与 cmd/ 出现即越界。
var importOfficialRe = regexp.MustCompile(`"(?:github\.com/[^"/]+/[^"/]+/)?(?:plugins/)?official(?:/[^"]*)?"`)

// checkDocs 检查文档链接可解析、文档地图完整、以及「核心数字」与生成结果一致。
//
// 设计文档约定「过期即缺陷」（design.md 文档约定）：这里的失败就是文档缺陷。
func checkDocs(root string) error {
	docsDir := filepath.Join(root, "docs")
	entries, err := os.ReadDir(docsDir)
	if err != nil {
		return fmt.Errorf("verify: 读取 docs/ 失败: %w", err)
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)

	linkRe := regexp.MustCompile(`\]\(([^)\s]+?)(?:#[^)]*)?\)`)
	indexRe := regexp.MustCompile(`^\|\s*\[([^\]]+)\]\(([^)]+)\)\s*\|`)

	var problems []string
	linked := map[string]bool{}
	for _, name := range files {
		raw, err := os.ReadFile(filepath.Join(docsDir, name))
		if err != nil {
			return err
		}
		text := string(raw)
		for _, m := range linkRe.FindAllStringSubmatch(text, -1) {
			target := m[1]
			if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") ||
				strings.HasPrefix(target, "mailto:") || strings.HasPrefix(target, "#") {
				continue
			}
			if strings.HasPrefix(target, "E:/") || strings.HasPrefix(target, "E:\\") || filepath.IsAbs(target) {
				problems = append(problems, fmt.Sprintf("%s 使用了绝对路径链接 %q（应使用仓库相对路径）", name, target))
				continue
			}
			clean := filepath.Clean(filepath.Join(docsDir, filepath.FromSlash(target)))
			if _, err := os.Stat(clean); err != nil {
				problems = append(problems, fmt.Sprintf("%s 的链接指向不存在的文件：%s", name, target))
				continue
			}
			linked[filepath.Base(clean)] = true
		}
	}

	// 文档地图必须覆盖每份文档；新增文档不进索引就是漂移。
	indexRaw, err := os.ReadFile(filepath.Join(docsDir, "design.md"))
	if err != nil {
		return fmt.Errorf("verify: 读取文档索引失败: %w", err)
	}
	indexed := map[string]bool{}
	for _, line := range strings.Split(string(indexRaw), "\n") {
		if m := indexRe.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			indexed[filepath.Base(m[2])] = true
		}
	}
	for _, name := range files {
		if name == "design.md" {
			continue
		}
		if !indexed[name] {
			problems = append(problems, fmt.Sprintf("docs/%s 没有登记进 design.md 的文档地图", name))
		}
		if !linked[name] && name != "design.md" {
			problems = append(problems, fmt.Sprintf("docs/%s 没有任何文档链接到它（孤立文档）", name))
		}
	}

	problems = append(problems, checkCoreNumbers(root, files)...)

	if len(problems) > 0 {
		return fmt.Errorf("文档有 %d 处问题:\n  - %s", len(problems), strings.Join(problems, "\n  - "))
	}
	return nil
}

// numberClaimRe 抓取「N 个 XX」形式的核心数字声明。
var numberClaimRe = regexp.MustCompile(`(\d+)\s*个\s*([A-Za-z0-9\x{4e00}-\x{9fff}]+)`)

// checkCoreNumbers 校验文档声明的方法/钩子/插槽数量与生成结果一致。
func checkCoreNumbers(root string, files []string) []string {
	want := map[string]int{
		"RPC 方法": len(protoMethods()),
		"方法":     len(protoMethods()),
		"钩子点位":   len(hookPointNames()),
		"钩子点":    len(hookPointNames()),
		"钩子":     len(hookPointNames()),
		"前端插槽":   len(slotNames()),
		"插槽":     len(slotNames()),
		"端口":     12,
		"协议源文件":  1,
	}
	var problems []string
	for _, name := range files {
		raw, err := os.ReadFile(filepath.Join(root, "docs", name))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if !strings.Contains(line, "个") {
				continue
			}
			for _, m := range numberClaimRe.FindAllStringSubmatch(line, -1) {
				n := 0
				for _, r := range m[1] {
					n = n*10 + int(r-'0')
				}
				key := m[2]
				exp, ok := want[key]
				if !ok {
					continue
				}
				// 「N 个」后面紧跟的限定词命中表里才检查，避免误伤「3 个官方 provider」这类句子。
				if !strictAdjacentCount(line, m[2]) {
					continue
				}
				if n != exp {
					problems = append(problems, fmt.Sprintf("docs/%s 声明「%s %d 个」，实际是 %d（先改设计文档再改实现）", name, key, n, exp))
				}
			}
		}
	}
	return problems
}

// strictAdjacentCount 只认「核心数字」式的声明：数字与量词之间没有其它实词。
func strictAdjacentCount(line, key string) bool {
	switch key {
	case "RPC 方法", "钩子点位", "钩子点", "前端插槽", "协议源文件":
		return true
	case "方法", "钩子", "插槽", "端口":
		return strings.Contains(line, "核心数字") || strings.Contains(line, "个 RPC 方法")
	}
	return false
}

// checkBoundary 实现设计 1.5.5 的六件事。
func checkBoundary(root string) error {
	var problems []string

	type finding struct {
		file string
		why  string
	}
	var hits []finding

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			switch filepath.Base(path) {
			case ".git", "node_modules", "testdata", "dist":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		isBase := strings.HasPrefix(rel, "internal/") || strings.HasPrefix(rel, "app/") || strings.HasPrefix(rel, "cmd/")
		if !isBase {
			return nil // tools/ 是门禁与构建工具，允许知道插件 id
		}
		if strings.HasSuffix(rel, "_test.go") {
			return nil // 测试夹具与替身允许出现 id 字面量
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		text := string(raw)

		// 1. 基座（internal/、cmd/）不 import 官方插件包；app/ 是唯一例外（组合根）。
		if importOfficialRe.MatchString(text) && !strings.HasPrefix(rel, "app/") {
			hits = append(hits, finding{rel, "import 了官方插件包（plugins/official）"})
		}
		// 2. 基座源码不出现官方插件 id（注释里也拦：避免「文档先行」把 id 固化进基座）
		for _, id := range officialPluginIDs {
			if strings.Contains(text, id) {
				hits = append(hits, finding{rel, "出现官方插件 id " + id})
			}
		}
		// 3. 不存在平行的 Go 插件注册 API
		if m := registrationAPIRe.FindStringSubmatch(text); m != nil {
			hits = append(hits, finding{rel, "声明了 Go 侧注册入口 " + m[1] + "（扩展只能走 TS AgentAPI）"})
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("verify: 扫描基座源码失败: %w", err)
	}
	if len(hits) > 0 {
		for _, h := range hits {
			problems = append(problems, fmt.Sprintf("%s：%s", h.file, h.why))
		}
	}

	// 4. 依赖方向：domain 只用标准库；ports 只用标准库 + domain（设计 13 约束）。
	if err := checkDependencyDirection(root); err != nil {
		problems = append(problems, err.Error())
	}

	// 5. 零插件启动：空基座必须真的能装配成「无工具、无供应商、有空态提示」的运行态。
	if err := emptyBaseBoots(root); err != nil {
		problems = append(problems, err.Error())
	}

	if len(problems) > 0 {
		return fmt.Errorf("基座边界有 %d 处越界:\n  - %s", len(problems), strings.Join(problems, "\n  - "))
	}
	return nil
}
