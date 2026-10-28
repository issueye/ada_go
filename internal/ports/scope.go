package ports

import (
	"context"
)

// PathView 是 Scope 返回的受限虚拟路径视图。
// 脚本里的工作区挂在 `/`：Virtual 是插件/UI 可见的路径，Real 是解析后的真实落点，
// 只在适配器内部使用，绝不回传给插件（09 §9.2：插件不能自己拼接路径绕过 Scope）。
type PathView struct {
	// Virtual 是规范化后的工作区相对路径（如 /src/main.go）。
	Virtual string
	// Real 是解析后的真实落点路径；只在宿主/适配器内部使用，绝不回传给插件脚本。
	Real string
	// WithinWorkspace 报告真实落点是否仍在工作区内（越界的 Resolve 返回错误，不为 true）。
	WithinWorkspace bool
	// ReadOnly 标记只读区域（锁目录 <dataDir>/locks/* 永远不在可写范围内，04 §5.7）。
	ReadOnly bool
}

// Scope 是路径解析与越界拒绝端口（01 §5.2：realpath + 平台规则；09 §9.2 是规则真源）。
// 语义约束：
//   - 先做字符串检查（拒绝 ..、绝对路径越界、非法字符），再 EvalSymlinks 解析真实落点；
//     工作区内指向外面的 symlink/junction 按真实落点拒绝（09 §9.2）；
//   - 唯一入口：宿主 fs/exec API 内部也走本端口；
//   - 读工具可读工作区；读插件目录/配置目录需要额外能力位；密钥目录永不授予（09 §9.2）；
//   - Windows 折叠盘符与大小写、统一分隔符（身份键同规则，01 §4.2）。
//
// 生产实现：internal/adapters/scope（realpath + 平台规则，M1-F/M5）。
// 测试替身：internal/testsupport 的内存 scope（合成 symlink 环与越界样本）。
type Scope interface {
	// Resolve 把插件侧虚拟路径解析为受限视图；越界或指向工作区外时
	// 返回 domain.CodePluginDenied（能力/信任不允许）错误，拒绝按真实落点判定（09 §9.2）。
	Resolve(ctx context.Context, virtualPath string) (PathView, error)
	// CheckExec 校验拟执行命令的 cwd 与可执行文件落点；命令能逃出路径沙箱是
	// 明确保留的口子，策略由“官方默认策略插件”负责（09 §9.2、03 §5.6）。
	CheckExec(ctx context.Context, cwd, command string) (PathView, error)
}
