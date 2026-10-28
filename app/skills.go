package app

// 本文件是技能的装配期结算（06 §6.7）：
//
//   - 技能表：插件贡献（host.Load 装配期扫描 Script 里的 skills/<name>/SKILL.md，
//     Contribution.Skills）+ 工作区 <workspace>/.adago/skills/<name>/SKILL.md；
//     同名冲突工作区优先并记录（不静默覆盖）。
//   - 基座工具 read_skill：产品 capabilities.skills=true 且至少有一个技能时随装配注册
//     （它不是插件贡献，是技能产品能力门控的基座工具）；按需返回 SKILL.md 正文，
//     截断到 16KB（渐进披露：装配期只注入 name/description，正文只在本表）。
//   - 插件侧的只读技能通道：hc.Skills（Session 注入）拿到的表与本表同源。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/plugins/host"
)

// MaxSkillBodyBytes 是 read_skill / skill.read 返回正文的截断阈值（6.7 渐进披露）。
const MaxSkillBodyBytes = 16 << 10

// baseToolPluginID 是基座自带工具在目录里的贡献者标识：它不是任何插件
// （官方插件 id 由产品声明选择，基座源码不出现），只用于回执与审计追溯。
const baseToolPluginID = "base"

// readSkillToolName 是技能正文读取工具的固定名字（spec skill.read 的模型侧入口）。
const readSkillToolName = "read_skill"

// skillsDirRel 是工作区技能目录（相对工作区根）。
var skillsDirRel = filepath.Join(".adago", "skills")

// skillTable 是一次装配的技能快照：按名字排序的元数据、正文表与同名冲突。
// 装配完成后不可变，可安全并发读。正文只进 read_skill / skill.read，
// 不进 Skills() 返回的元数据（渐进披露：装配期只注入 name/description）。
type skillTable struct {
	metas     []domain.SkillMeta
	byName    map[string]domain.SkillMeta
	bodies    map[string]string
	conflicts []string
}

func (t skillTable) empty() bool { return len(t.metas) == 0 }

// metasForPrompt 返回注入系统提示与插件通道的元数据（天然不含正文）。
func (t skillTable) metasForPrompt() []domain.SkillMeta {
	return append([]domain.SkillMeta(nil), t.metas...)
}

// buildSkillTable 合并插件技能与工作区技能：同名冲突工作区优先，冲突名记入
// conflicts（不静默覆盖：冲突必须有可见记录，6.7）。输出按名字排序。
func buildSkillTable(pluginSkills, workspaceSkills []host.Skill) skillTable {
	type entry struct {
		meta domain.SkillMeta
		body string
	}
	merged := map[string]entry{}
	var conflicts []string
	for _, s := range pluginSkills {
		if s.Meta.Name == "" {
			continue
		}
		if _, dup := merged[s.Meta.Name]; dup {
			conflicts = append(conflicts, s.Meta.Name)
			continue
		}
		merged[s.Meta.Name] = entry{s.Meta, s.Body}
	}
	for _, s := range workspaceSkills {
		if s.Meta.Name == "" {
			continue
		}
		if _, dup := merged[s.Meta.Name]; dup {
			conflicts = append(conflicts, s.Meta.Name)
		}
		merged[s.Meta.Name] = entry{s.Meta, s.Body} // 工作区优先
	}
	metas := make([]domain.SkillMeta, 0, len(merged))
	bodies := make(map[string]string, len(merged))
	byName := make(map[string]domain.SkillMeta, len(merged))
	for name, e := range merged {
		metas = append(metas, e.meta)
		byName[name] = e.meta
		bodies[name] = e.body
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].Name < metas[j].Name })
	return skillTable{metas: metas, byName: byName, bodies: bodies, conflicts: conflicts}
}

// loadWorkspaceSkills 扫描 <workspace>/.adago/skills/<name>/SKILL.md。
// 单个技能非法记警告并跳过（不中断）；目录不存在是常态，不是错误。
func loadWorkspaceSkills(workspace string, warn func(string)) []host.Skill {
	root := filepath.Join(workspace, skillsDirRel)
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var skills []host.Skill
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(root, e.Name(), "SKILL.md"))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				warn("技能目录 " + e.Name() + " 缺少 SKILL.md，已跳过")
				continue
			}
			warn("读取技能 " + e.Name() + " 的 SKILL.md 失败: " + err.Error())
			continue
		}
		meta, body, err := host.ParseSkill(e.Name(), string(raw))
		if err != nil {
			warn("技能 " + e.Name() + " 解析失败，已跳过: " + err.Error())
			continue
		}
		meta.Origin = host.SkillOriginWorkspace
		skills = append(skills, host.Skill{Meta: meta, Body: body})
	}
	sort.Slice(skills, func(i, j int) bool { return skills[i].Meta.Name < skills[j].Meta.Name })
	return skills
}

// settleSkills 是装配期的技能结算：picked 是本次装配实际加载的插件脚本集合
// （产品声明过滤 + 第三方追加之后）。工作区技能在同名冲突时覆盖插件技能。
func settleSkills(picked []host.Script, workspace string, warn func(string)) skillTable {
	var pluginSkills []host.Skill
	for _, sc := range picked {
		skills, warns := host.ScriptSkills(sc)
		pluginSkills = append(pluginSkills, skills...)
		for _, w := range warns {
			warn(w)
		}
	}
	return buildSkillTable(pluginSkills, loadWorkspaceSkills(workspace, warn))
}

// newReadSkillTool 构造基座工具 read_skill：access=read、terminatesTurn=false、
// 参数 {skill}。执行读取本装配技能表里的正文（16KB 截断）。
func newReadSkillTool(table skillTable) BaseTool {
	return BaseTool{
		Descriptor: domain.Descriptor{
			Name:        readSkillToolName,
			Description: "读取一个可用技能的完整正文（SKILL.md）。系统提示里只注入了技能名与简介；需要按某个技能的指引操作时，先用本工具读取它的正文。",
			Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "skill": { "type": "string", "description": "技能名（等于技能目录名，见系统提示里的可用技能列表）" }
  },
  "required": ["skill"]
}`),
			Access:         domain.AccessRead,
			Timeout:        5 * time.Second,
			TerminatesTurn: false,
			PluginID:       baseToolPluginID,
		},
		Invoke: func(_ context.Context, args json.RawMessage) (domain.Result, error) {
			return invokeReadSkill(table, args)
		},
	}
}

// invokeReadSkill 是 read_skill 的实现（与 skill.read RPC 共用 truncateSkillBody）。
func invokeReadSkill(table skillTable, args json.RawMessage) (domain.Result, error) {
	var p struct {
		Skill string `json:"skill"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &p); err != nil {
			return domain.Result{}, fmt.Errorf("read_skill: 参数不是合法 JSON: %w", err)
		}
	}
	name := strings.TrimSpace(p.Skill)
	if name == "" {
		return domain.Result{}, errors.New("read_skill: 需要非空的 skill 参数（可用技能见系统提示）")
	}
	meta, ok := table.byName[name]
	if !ok {
		return domain.Result{}, fmt.Errorf("read_skill: 未知技能 %q（用 skill.list 或系统提示查看可用技能）", name)
	}
	content, truncated := truncateSkillBody(table.bodies[name])
	text := content
	if truncated {
		text += "\n\n…（技能正文超过 16KB，已截断）"
	}
	return domain.Result{
		Content: []domain.Part{{Type: domain.PartText, Text: text}},
		Details: map[string]any{"skill": name, "origin": meta.Origin, "truncated": truncated},
	}, nil
}

// truncateSkillBody 把正文截到 MaxSkillBodyBytes（不在多字节字符中间断开）。
func truncateSkillBody(body string) (string, bool) {
	if len(body) <= MaxSkillBodyBytes {
		return body, false
	}
	cut := MaxSkillBodyBytes
	for cut > 0 && !utf8.RuneStart(body[cut]) {
		cut--
	}
	return body[:cut], true
}
