package host

// 本文件是技能发现（06 §6.7）：插件包内 skills/<name>/SKILL.md 的扫描与 frontmatter 解析。
//
// 边界：
//   - 装配期只解析出 name/description（进系统提示的 XML 片段）；正文与元数据一起
//     交给装配方（app 的技能表），正文只经基座 read_skill / skill.read 按需披露，
//     不进 RPC 摘要、不进日志（渐进披露）。
//   - 单个 SKILL.md 的问题（缺字段、名字与目录不符、超长）跳过并记警告，不中断装配
//     ——与插件发现层「其余插件不受影响」同一原则；不存在任何技能不是错误。
//   - 正文来源是 Script.StaticAssets（键 = 相对路径 skills/<name>/SKILL.md）：第三方
//     插件由 discovery.sideFiles 收集，官方插件由 embed.go 收集，两条路径形状一致。

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/adag/ada-go/internal/domain"
)

// 技能来源（domain.SkillMeta.Origin 的取值；6.7 的两个目录）。
const (
	// SkillOriginPlugin 表示技能来自插件包内 skills/<name>/SKILL.md。
	SkillOriginPlugin = "plugin"
	// SkillOriginWorkspace 表示技能来自 <workspace>/.adago/skills/<name>/SKILL.md。
	SkillOriginWorkspace = "workspace"
)

// skillNameRe 按 Agent Skills 约定限定技能名：1–64，小写字母/数字/连字符（6.7）。
var skillNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

const (
	// SkillAssetPrefix / SkillAssetSuffix 界定 StaticAssets 里的技能文件键。
	SkillAssetPrefix = "skills/"
	SkillAssetSuffix = "/SKILL.md"
	// MaxSkillDescription 是 description 的长度上限（6.7：不超过 1024 字符）。
	MaxSkillDescription = 1024
	// MaxSkillFileBytes 是单个 SKILL.md 的大小上限：技能是提示料不是数据集，
	// 超限视为打包错误（正文另有 16KB 的读取截断，见 app 的 read_skill）。
	MaxSkillFileBytes = 256 << 10
)

// Skill 是一次扫描的产物：元数据 + SKILL.md 正文（frontmatter 之后）。
// 正文不进 Contribution.Skills（那里只放元数据），供装配方建 read_skill 的数据表。
type Skill struct {
	Meta domain.SkillMeta
	Body string
}

// ParseSkill 解析一份 SKILL.md：frontmatter 取 name/description，正文取分隔线之后。
//
// dirName 是技能目录名（Agent Skills 约定：frontmatter 的 name 必须等于它）。
// origin/pluginId 由调用方补齐；这里只做形状校验。frontmatter 是「简单 YAML」：
// 只认顶层 `key: value` 行与单/双引号包裹的值，不支持嵌套与多行——技能约定只用到这两项。
func ParseSkill(dirName, raw string) (domain.SkillMeta, string, error) {
	name := strings.TrimSpace(dirName)
	if !skillNameRe.MatchString(name) {
		return domain.SkillMeta{}, "", fmt.Errorf("技能目录名 %q 不满足 1–64 位小写字母/数字/连字符", dirName)
	}
	if len(raw) > MaxSkillFileBytes {
		return domain.SkillMeta{}, "", fmt.Errorf("SKILL.md 超过 %d 字节上限", MaxSkillFileBytes)
	}
	front, body, err := splitFrontmatter(raw)
	if err != nil {
		return domain.SkillMeta{}, "", err
	}
	fields := parseFrontmatterFields(front)
	desc := ""
	if v, ok := fields["description"]; ok {
		desc = v
	}
	if desc == "" {
		return domain.SkillMeta{}, "", fmt.Errorf("frontmatter 缺少 description（6.7）")
	}
	if r := []rune(desc); len(r) > MaxSkillDescription {
		return domain.SkillMeta{}, "", fmt.Errorf("description 超过 %d 字符", MaxSkillDescription)
	}
	if v, ok := fields["name"]; ok && v != name {
		return domain.SkillMeta{}, "", fmt.Errorf("frontmatter name %q 与目录名 %q 不一致（Agent Skills 约定）", v, name)
	}
	return domain.SkillMeta{Name: name, Description: desc}, body, nil
}

// splitFrontmatter 按 `---` 开头/结尾两行切出 frontmatter；没有 frontmatter 是错误
// （没有 description 就无法做渐进披露的第一层）。
func splitFrontmatter(raw string) (front, body string, err error) {
	text := strings.ReplaceAll(raw, "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") && text != "---" {
		return "", "", fmt.Errorf("缺少 YAML frontmatter（文件必须以 --- 行开头）")
	}
	rest := strings.TrimPrefix(text, "---\n")
	if rest == text {
		rest = "" // 整个文件就是 "---"
	}
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return "", "", fmt.Errorf("frontmatter 没有闭合的 --- 行")
	}
	after := rest[end+len("\n---"):]
	// 闭合行后面最多允许一行空白与换行（---\n 或 --- 与文件尾）。
	if after != "" && !strings.HasPrefix(after, "\n") {
		return "", "", fmt.Errorf("frontmatter 闭合行 --- 之后必须换行")
	}
	return rest[:end], strings.TrimPrefix(after, "\n"), nil
}

// parseFrontmatterFields 读出 frontmatter 里所有顶层 `key: value`（首个同名键生效）。
func parseFrontmatterFields(front string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(front, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			continue
		}
		if len(value) >= 2 && (value[0] == '"' && value[len(value)-1] == '"' || value[0] == '\'' && value[len(value)-1] == '\'') {
			value = value[1 : len(value)-1]
		}
		if _, dup := out[key]; !dup {
			out[key] = value
		}
	}
	return out
}

// ScriptSkills 扫描一个插件加载单元里的全部技能（origin=plugin）。
//
// warnings 收集被跳过的技能及原因（调用方决定记到哪里）。Skill.Meta.PluginID 取
// 清单 id；清单缺失时置空（Load 在校验阶段就拒绝了缺清单的脚本）。
func ScriptSkills(script Script) (skills []Skill, warnings []string) {
	id := ""
	if script.Manifest != nil {
		id = script.Manifest.ID
	}
	for key, raw := range script.StaticAssets {
		rel := strings.TrimPrefix(key, "./")
		if !strings.HasPrefix(rel, SkillAssetPrefix) || !strings.HasSuffix(rel, SkillAssetSuffix) {
			continue
		}
		mid := strings.TrimSuffix(strings.TrimPrefix(rel, SkillAssetPrefix), SkillAssetSuffix)
		if mid == "" || strings.Contains(mid, "/") {
			warnings = append(warnings, fmt.Sprintf("技能文件 %s 的路径形状非法（期望 skills/<name>/SKILL.md），已跳过", rel))
			continue
		}
		meta, body, err := ParseSkill(mid, string(raw))
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("技能 %s 解析失败，已跳过: %v", rel, err))
			continue
		}
		meta.Origin = SkillOriginPlugin
		meta.PluginID = id
		skills = append(skills, Skill{Meta: meta, Body: body})
	}
	// StaticAssets 是 map：按名字排序，保证同一装配下技能顺序可复现。
	sort.Slice(skills, func(i, j int) bool { return skills[i].Meta.Name < skills[j].Meta.Name })
	return skills, warnings
}
