// Package ports 是 Core 的 12 个端口接口（01 §5.2 端口表，一个不多一个不少）：
// Clock、IDGen、EventSink、SessionStore、ModelClient、ToolCatalog、
// InteractionBroker、HookRunner、Scope、ScriptHost、StateStore、SecretBroker。
//
// 依赖规则（硬门禁，01 §3.3）：
//   - ports 只 import domain 与标准库；不 import 任何适配器，不出现 moejs/sqlite/gin 的影子；
//   - 本包只有接口与其请求/响应数据结构，零实现（M0 契约冻结）；
//   - domain 不知道 Gin，engine 不知道 moejs：引擎细节属于 adapters/plugins 包。
//
// INV-2（02 §5.3）：每个端口都有生产实现与测试替身，不允许 nil 端口；
// 各端口注释标明了计划实现包（里程碑见 development-plan §4.2）。
//
// 明确不造端口的东西（01 §5.2）：CancelToken 直接用 context.Context；
// PromptSource 是 engine 内部函数；BlobStore 等真实需求出现再加。
// Gateway↔Core 的 RPC 面属于 coreproto（M2），不是端口；握手协议版本校验用
// internal/version.CheckCompatible（version 依赖 domain，ports 不依赖 version）。
package ports
