// Package web 是 Gateway 的 Web 宿主。整页 SSR 移除后，这里只剩
// shell/login/error 三个模板、JSON 数据端点与插件静态资源/动作桥。
//
// 插槽集合是稳定的兼容承诺（设计 7.2）：12 个名字由 internal/common/pluginapi
// 声明（冻结契约）。宿主模板的概念已随 SSR 删除；本文件保留的只是
// 「词汇表消费侧」的一致性校验，供 adag verify 复用。
package web

import (
	"fmt"
	"strings"

	"github.com/adag/ada-go/internal/common/pluginapi"
)

// CheckSlotCoverage 校验插槽词汇表自身的一致性：
// 名字非空且唯一、Slots 与 SlotNames/LookupSlot 互相一致。
// 任何声明插槽的地方（清单 slots[]、前端注册表）都必须落在这 12 个名字里
// ——那是 pluginapi.IsSlotName 的职责；这里守护词汇表本身没有内部漂移。
func CheckSlotCoverage() error {
	slots := pluginapi.Slots()
	if len(slots) == 0 {
		return fmt.Errorf("web: 插槽词汇表为空")
	}
	seen := make(map[string]bool, len(slots))
	for _, s := range slots {
		if strings.TrimSpace(s.Name) == "" {
			return fmt.Errorf("web: 插槽词汇表存在空名字")
		}
		if seen[s.Name] {
			return fmt.Errorf("web: 插槽 %q 重复声明", s.Name)
		}
		seen[s.Name] = true
	}
	for _, s := range slots {
		got, ok := pluginapi.LookupSlot(s.Name)
		if !ok {
			return fmt.Errorf("web: 插槽 %q 无法经 LookupSlot 反查", s.Name)
		}
		if got.Name != s.Name {
			return fmt.Errorf("web: 插槽 %q 反查结果不一致（得到 %q）", s.Name, got.Name)
		}
		if !pluginapi.IsSlotName(s.Name) {
			return fmt.Errorf("web: 插槽 %q 未被 IsSlotName 识别", s.Name)
		}
	}
	if names := pluginapi.SlotNames(); len(names) != len(slots) {
		return fmt.Errorf("web: SlotNames 与 Slots 数量不一致（%d vs %d）", len(names), len(slots))
	}
	return nil
}
