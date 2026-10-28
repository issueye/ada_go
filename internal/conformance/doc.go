// Package conformance 是 M0「契约冻结」的一致性门禁与测试基础设施（development-plan §4.1 M0.5）。
//
// 它只做两件事：
//  1. 用契约测试把「同一份契约散落在多处」的地方咬死 —— 错误码、事件名、清单、
//     VNode 白名单、产品声明、端口签名（本包目录下的 *_test.go）；
//  2. 提供可复用的测试替身与夹具工厂（子包 testkit：脚本化模型、事件记录器、
//     假时钟、确定性 id、临时工作区、交互桩），M1 起的引擎/存储/provider 测试直接复用。
//
// 边界：
//   - 本包（含 testkit）不 import official/，也不在非测试代码里出现任何官方插件 id；
//     official id 只允许作为 testdata 里的清单夹具存在（它本身就是插件清单）。
//   - internal/conformance/nodecompat 属于 M1（Node 20 双跑语料），不在本包范围。
//
// CI 门禁（development-plan §7.1、10 §11.2）用下面这条命令跑不变量套件：
//
//	go test ./internal/conformance -run TestInvariants        # INV-1…INV-12 骨架套件
//	go test ./internal/conformance/...                        # 全部契约测试
//	go test ./internal/conformance -race                      # 替身并发安全
//	GOWORK=off go test ./internal/conformance/...              # 独立 module 模式同样必须通过
//
// -run TestInvariants 是不锚定的正则匹配，命中的就是下面 invariants_test.go 里的
// TestInvariants（子测试 INV-1…INV-12）。现在无法真实断言的不变量在子测试里
// 用 t.Skip("M1-…: …尚未落地") 明说，不用永真断言冒充覆盖。
//
// golden 基线在 testdata/golden/*.txt；重新生成用
// ADAG_GOLDEN_UPDATE=1 go test ./internal/conformance -run TestEventGolden
// （生成后必须人工复核再提交，规则同 go generate 的 git diff 门禁）。
package conformance
