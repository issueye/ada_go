package ports

import "context"

// SecretBroker 是按名字取密钥、日志脱敏的端口（01 §5.2：实现为环境/文件）。
// 语义约束（09 §9.4 数据边界）：
//   - 密钥只存在于 Core 的内存与配置；不进入提示词、工具结果、日志、前端 props，
//     也不进入 process.env 或插件启动的子进程环境；
//   - 只有清单声明了 secrets 的插件才能按名字取值（03 §5.5 ProviderContext.secret、
//     05 §6.3 ToolContext.secret —— 未声明时属性不存在，不是返回空串）；
//   - Gateway 不接触密钥（01 §4.1：不持有模型密钥）；供应商配置页只显示“已配置 + 尾号”（07 §7.7.6）。
//
// 生产实现：internal/adapters/secret（环境/文件，M1-B）。
// 测试替身：internal/testsupport 的内存密钥环（INV-2；脱敏断言用）。
type SecretBroker interface {
	// Lookup 按名字取密钥。found=false 表示清单未声明或配置缺失；
	// 返回值绝不会被实现方写入日志（本接口以约定强制，而非运行时检查）。
	Lookup(ctx context.Context, name string) (secret string, found bool, err error)
	// Declared 报告名字是否已被加载插件清单声明（宿主按名字授权，越名即拒）。
	Declared(ctx context.Context, pluginID, name string) bool
	// Redact 对任意文本做已知密钥脱敏（日志与错误信息出口统一调用；01 §5.2“日志脱敏”）。
	Redact(ctx context.Context, text string) string
}
