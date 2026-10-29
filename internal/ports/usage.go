package ports

// UsageAggregate 是用量聚合的端口类型（08 usage.get）。
//
// 放在 ports 而不是 SQLite 适配器里：RPC 层只应知道「用量怎么汇总」，
// 不应反向依赖某个存储实现。适配器返回这个类型，测试替身也能直接构造。
type UsageAggregate struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Input    int64  `json:"inputTokens"`
	Output   int64  `json:"outputTokens"`
	Cache    int64  `json:"cacheTokens"`
	Cost     int64  `json:"costMicros"`
}
