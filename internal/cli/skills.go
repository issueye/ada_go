package cli

// 本文件是技能的会话侧接线（06 §6.7）。
//
// 技能表本身在装配期由 app 结算（app.settleSkills：插件贡献 + 工作区
// .adago/skills，同名工作区优先；read_skill 由 app 按产品 capabilities.skills
// 与非空表注册）。这里只做三件事：
//
//   - 把装配的技能表接成 engine.Deps.Skills（产品 capabilities.skills=true 时）；
//   - 把各插件 registerPrompt 的贡献接成 engine.Deps.Prompts
//     （引擎按 priority/pluginID 稳定排序，安全前言永远在最前）；
//   - 给 skill.list / skill.read RPC 提供数据源（与基座 read_skill 同一实现）。

import (
	"context"

	"github.com/adag/ada-go/app"
	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/engine"
)

// logWarn 把警告送进日志通道（未配置日志时丢弃）。
func (s *Session) logWarn(msg string) {
	if s.cfg.Log != nil {
		s.cfg.Log("warn", msg, nil)
	}
}

// SkillSummaries 返回当前装配的技能元数据与同名冲突（skill.list 数据源）。
func (s *Session) SkillSummaries() ([]domain.SkillMeta, []string) {
	asm := s.Assembly() // 取一次快照：元数据与冲突必须来自同一代装配
	return asm.Skills(), asm.SkillConflicts()
}

// ReadSkill 读取一个技能的元数据与正文（skill.read 数据源；基座 read_skill 走同一实现）。
// 第四个返回值为 false 表示技能不存在；正文已按 16KB 截断并带标记。
func (s *Session) ReadSkill(name string) (domain.SkillMeta, string, bool, bool) {
	return s.Assembly().ReadSkill(name)
}

// skillsMetaSnapshot 是 host.Context.Skills 的实现：给插件的只读元数据
// （不含正文；正文只能走 read_skill）。快照随装配切换。
func (s *Session) skillsMetaSnapshot() []domain.SkillMeta {
	return s.Assembly().Skills()
}

// sessionSkillSource 把装配的技能表适配成 engine.SkillSource。
type sessionSkillSource struct{ s *Session }

func (src sessionSkillSource) Metas(context.Context) []domain.SkillMeta {
	return src.s.skillsMetaSnapshot()
}

// assemblyPromptSource 把装配里各插件 registerPrompt 的贡献适配成 engine.PromptSource。
// 排序由引擎负责（priority 越小越靠前，其次按插件 id），安全前言永远在最前。
type assemblyPromptSource struct{ frags []engine.PromptFragment }

func (src assemblyPromptSource) Fragments(context.Context) []engine.PromptFragment {
	return src.frags
}

// promptFragmentsOf 从装配结果收集提示词片段（persona 等提示词插件的贡献）。
func promptFragmentsOf(asm *app.Assembly) []engine.PromptFragment {
	var frags []engine.PromptFragment
	if asm == nil {
		return nil
	}
	for _, inst := range asm.Plugins {
		c := inst.Contribution()
		if c == nil {
			continue
		}
		for _, p := range c.Prompts {
			frags = append(frags, engine.PromptFragment{
				PluginID: inst.ID(), Priority: p.Priority, Content: p.Content,
			})
		}
	}
	return frags
}
