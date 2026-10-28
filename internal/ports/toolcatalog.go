package ports

import (
	"context"

	"github.com/adag/ada-go/internal/domain"
)

// ToolCatalog 暴露【当前可用】的工具描述符目录（01 §5.2：装配期快照，来源：插件）。
// 语义约束：
//   - INV-3：描述符注册表是唯一真源，目录由它派生（02 §5.3）；
//   - 目录在装配期冻结；运行期新登记只记日志、不生效，要生效必须重载插件（02 §5.4、06 §6.5）；
//   - 基座不注册任何工具，空基座的 List 返回空（INV-11：禁用插件后能力必须消失，基座不补位）。
//
// 生产实现：internal/plugins 装配期结算 + internal/toolkit 注册表（M1-A/M3）。
// 测试替身：internal/testsupport 的静态目录（fake provider E2E 注入桩描述符，development-plan M1-A）。
type ToolCatalog interface {
	// List 返回装配期冻结的全量描述符（稳定排序：注册序 + 名称），供模型请求与 tool.list 共用。
	List(ctx context.Context) []domain.Descriptor
	// Lookup 按名称查描述符；不存在（含被禁用）返回 false —— 对应 tool_not_found（08 §8.5）。
	Lookup(ctx context.Context, name string) (domain.Descriptor, bool)
}
