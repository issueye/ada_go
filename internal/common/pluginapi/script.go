package pluginapi

// 本文件是「待加载插件的编译产物」契约（06 §6.4 初始化、06 §6.5 装配、12 §6.12.3 解析）。
//
// 它曾经住在插件宿主（internal/agent_core/plugins/host）。Gateway 的前端渲染桥只需要
// 「清单 + 模板源码 + 静态资源」这三样事实，不需要 moejs Runtime、不需要宿主句柄；
// 把类型下沉到这里后，gateway 不再依赖插件宿主（2026-11-10 的分层收敛）。

import (
	"fmt"
	"strings"
)

// EntryScheme 是虚拟模块的名字空间（06 §6.4）。
const EntryScheme = "plugin"

// Script 是一个待加载插件的编译产物与清单。
//
// Source 是 esbuild 之后的 ESM bundle（官方插件构建期预编译，第三方安装期编译）；
// 宿主不做 TypeScript 编译，也不做磁盘 node_modules 解析（6.10）。这个类型只承载
// 「清单 + 源码 + 静态资源」三样事实，不含任何 Runtime 句柄，因此 Core 与 Gateway
// 可以共用同一份形状。
type Script struct {
	Manifest *Manifest
	// EntryName 是入口模块的虚拟名，形如 plugin://<id>/index.js。
	EntryName string
	// Source 是入口模块源码。
	Source string
	// Bundle 是随包的其它模块（specifier → 源码）；loader 只在其中解析，不落盘。
	Bundle map[string]string
	// StaticAssets 是插件包内静态资源的只读视图（plugin://<id>/…）。
	StaticAssets map[string][]byte
	// Schemas 是前端 block 数据 Schema 的原文（清单路径 → JSON 字节）。
	Schemas map[string][]byte
	// Root 是插件包在宿主上的绝对目录（可选）；技能路径按它拼出 SKILL.md 的绝对路径。
	Root string
	// Readme 是插件包内 README.md 的原文（可选；详情页展示用）。
	Readme string
}

// AgentAPIModule 是类型声明用的虚拟模块名（05 §6.3：后端插件从 agent:api 导入类型）。
// esbuild 会擦除纯类型导入，运行期这里只兜住「显式 import 它」的形状：导出为空。
const AgentAPIModule = "agent:api"

// EntryName 拼出规范入口名 plugin://<id>/index.js。
func EntryName(id string) string { return EntryScheme + "://" + id + "/index.js" }

// CheckVirtualName 限定模块名必须落在 plugin://<id>/ 之下（不接受绝对宿主路径）。
func CheckVirtualName(id, name string) error {
	if name == AgentAPIModule {
		return nil
	}
	rest, ok := strings.CutPrefix(name, EntryScheme+"://")
	if !ok {
		return fmt.Errorf("必须是 %s://<id>/… 形状的虚拟名", EntryScheme)
	}
	owner, _, _ := strings.Cut(rest, "/")
	if owner != id {
		return fmt.Errorf("模块名属于插件 %q，与当前插件 %q 不符", owner, id)
	}
	if strings.Contains(name, "..") {
		return fmt.Errorf("模块名不允许包含 ..")
	}
	return nil
}

// PluginID 从清单或入口名取插件 id（清单优先）。
func (s Script) PluginID() string {
	if s.Manifest != nil && s.Manifest.ID != "" {
		return s.Manifest.ID
	}
	if rest, ok := strings.CutPrefix(s.EntryName, EntryScheme+"://"); ok {
		id, _, _ := strings.Cut(rest, "/")
		return id
	}
	return ""
}

// CheckEntryName 校验入口名落在当前插件的虚拟名字空间内。
func (s Script) CheckEntryName(id string) error {
	if err := CheckVirtualName(id, s.EntryName); err != nil {
		return fmt.Errorf("插件 %s 的入口名 %q 非法: %w", id, s.EntryName, err)
	}
	return nil
}

// Validate 检查待加载脚本的形状：空基座不崩，缺件要点名（06 §6.5）。
//
// 它只做形状检查（id/apiVersion/trust/入口名/随包模块名），不编译、不执行脚本；
// 模块链接与静态导入检查留在插件宿主（那需要 moejs 与 esbuild）。
func (s Script) Validate() error {
	if s.Manifest == nil {
		return fmt.Errorf("插件脚本缺少清单")
	}
	id := s.Manifest.ID
	if !ValidID(id) {
		return fmt.Errorf("插件 id %q 非法（^[a-z][a-z0-9-]{1,63}$，06 §6.2）", id)
	}
	if s.Manifest.APIVersion != APIVersionMajor {
		return fmt.Errorf("插件 %s 的 apiVersion %d 与宿主主版本 %d 不匹配（06 §6.2）",
			id, s.Manifest.APIVersion, APIVersionMajor)
	}
	if !s.Manifest.Trust.Valid() {
		return fmt.Errorf("插件 %s 的 trust %q 未知（06 §6.9）", id, s.Manifest.Trust)
	}
	if strings.TrimSpace(s.Source) == "" {
		return fmt.Errorf("插件 %s 没有入口源码（backend.entry 为空或在 Bundle 里缺失，06 §6.2）", id)
	}
	if s.EntryName == "" {
		s.EntryName = EntryName(id)
	}
	if err := s.CheckEntryName(id); err != nil {
		return err
	}
	for name := range s.Bundle {
		if err := CheckVirtualName(id, name); err != nil {
			return fmt.Errorf("插件 %s 的随包模块 %q 非法: %w", id, name, err)
		}
	}
	return nil
}
