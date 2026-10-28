// Package nodecompat 是 M1-D「Node 兼容子集尖刺」的持久产物：兼容性验证语料、
// moejs v0.1.0-alpha.11 的真实偏差表，以及把两者与安装矩阵对账的门禁测试。
//
// 它属于设计 12 §6.14 的兼容性验证层，不是运行时组件：
//   - 不注册工具、不提供 provider、不读产品配置，也不出现任何官方插件 id（§6.10 末段、D-5）；
//   - 不实现 Node 模块，也不提供 stdlib 安装：真正的安装层在 M1-C 的宿主里
//     （internal/plugins/host/node 只携带矩阵与错误码这些数据）；
//   - 除测试文件外没有可执行代码：doc.go 之外的 .go 文件全是 _test.go，
//     因此这个包不会被任何生产包 import，也不会给二进制增加体积。
//
// 三块内容：
//
//	deviations.md      偏差表：模块/成员 × Node 20 期望 × alpha.11 实测 × 归属（上游 / 宿主 / 不可用）
//	corpus/            §6.14.2 的九层 JS 语料 + 自报「返回值/错误 code/事件顺序/副作用」的 JSON 协议
//	corpus_test.go     TestNodeCompat：在 moejs Runtime 里跑语料，断言当前真实行为
//	matrix_test.go     安装矩阵与实测对账：TierNever 必须不存在，TierAlways 必须可导入且成员可枚举
//
// 证据全部可复现：语料结论来自这里，尖刺结论来自 go run ./tools/moejspike（同一条 Runtime
// 夹具、同一份 moejs 检出），两者不允许各写一套事实。
//
// CI 门禁（设计 12 §6.14.4）：
//
//	go test -count=1 ./internal/conformance/nodecompat -run TestNodeCompat
//	go test -count=1 ./internal/conformance/nodecompat
//	GOWORK=off go test -count=1 ./internal/conformance/nodecompat   # 发布口径（D-2）
//
// 基线重新生成：ADAG_NODECOMPAT_RECORD=1 go test ./internal/conformance/nodecompat -run TestNodeCompat
// —— 它只改 corpus/**/*.expect.json 的 result/observe 段，decision/reason/runtime/nodeParity 是人写的，
// 生成后必须人工复核再提交（同 internal/conformance/testdata/golden 的规矩）。
//
// 明确不做的事：上游没有的能力（node:child_process、node:module、node:timers/promises、zlib、
// 单次读写上限…）在这里一律 t.Skip 或断言「模块不存在」，不用永真断言冒充覆盖；
// 宿主侧才有的行为（execFile、16MB/10k 夹逼、成员裁剪调用即抛）标 skip-host 等 M1-C。
package nodecompat
