package app

// 本文件是技能的装配期结算（06 §6.7）：
//
//   - 技能表：插件贡献（host.Load 装配期扫描 Script 里的 skills/<name>/SKILL.md，
//     Contribution.Skills）+ 工作区 <workspace>/.adago/skills/<name>/SKILL.md；
//     同名冲突工作区优先并记录（不静默覆盖）。
//   - 只结算 name/description/absolute path：正文不进入基座内存，模型按系统提示里的
//     路径用 read_file 直接读取。
//   - 插件侧的只读技能通道：hc.Skills（装配方注入）拿到的表与本表同源。

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/adag/ada-go/internal/agent_core/plugins/host"
	"github.com/adag/ada-go/internal/common/domain"
)

// skillsDirRel 是工作区技能目录（相对工作区根）。
var skillsDirRel = filepath.Join(".adago", "skills")

// skillTable 是一次装配的技能快照：按名字排序的元数据（含 SKILL.md 绝对路径）与同名冲突。
// 装配完成后不可变，可安全并发读；正文不在表里。
type skillTable struct {
	metas     []domain.SkillMeta
	conflicts []string
}

// metasForPrompt 返回注入系统提示与插件通道的元数据（name/description/path）。
func (t skillTable) metasForPrompt() []domain.SkillMeta {
	return append([]domain.SkillMeta(nil), t.metas...)
}

// buildSkillTable 合并插件技能与工作区技能：同名冲突工作区优先，冲突名记入
// conflicts（不静默覆盖：冲突必须有可见记录，6.7）。输出按名字排序。
func buildSkillTable(pluginSkills, workspaceSkills []host.Skill) skillTable {
	merged := map[string]domain.SkillMeta{}
	var conflicts []string
	for _, s := range pluginSkills {
		if s.Meta.Name == "" {
			continue
		}
		if _, dup := merged[s.Meta.Name]; dup {
			conflicts = append(conflicts, s.Meta.Name)
			continue
		}
		merged[s.Meta.Name] = s.Meta
	}
	for _, s := range workspaceSkills {
		if s.Meta.Name == "" {
			continue
		}
		if _, dup := merged[s.Meta.Name]; dup {
			conflicts = append(conflicts, s.Meta.Name)
		}
		merged[s.Meta.Name] = s.Meta // 工作区优先
	}
	metas := make([]domain.SkillMeta, 0, len(merged))
	for _, meta := range merged {
		metas = append(metas, meta)
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].Name < metas[j].Name })
	return skillTable{metas: metas, conflicts: conflicts}
}

// loadWorkspaceSkills 扫描 <workspace>/.adago/skills/<name>/SKILL.md。
// 单个技能非法记警告并跳过（不中断）；目录不存在是常态，不是错误。
func loadWorkspaceSkills(workspace string, warn func(string)) []host.Skill {
	root := filepath.Join(workspace, skillsDirRel)
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var skills []host.Skill
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		path := filepath.Join(root, e.Name(), "SKILL.md")
		raw, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				warn("技能目录 " + e.Name() + " 缺少 SKILL.md，已跳过")
				continue
			}
			warn("读取技能 " + e.Name() + " 的 SKILL.md 失败: " + err.Error())
			continue
		}
		meta, err := host.ParseSkill(e.Name(), string(raw))
		if err != nil {
			warn("技能 " + e.Name() + " 解析失败，已跳过: " + err.Error())
			continue
		}
		meta.Origin = host.SkillOriginWorkspace
		meta.Path = path
		skills = append(skills, host.Skill{Meta: meta})
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
