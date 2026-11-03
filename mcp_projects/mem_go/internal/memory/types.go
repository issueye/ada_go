// Package memory 实现基于向量检索的 AI Agent 长期记忆引擎。
//
// 底层使用 GoVector（嵌入式向量数据库，BoltDB + HNSW）持久化记忆，
// 每条记忆对应一个带 payload 的向量点，支持语义去重、标签过滤、
// 重要度加权与时间衰减重排。
package memory

import (
	"fmt"
	"strings"
	"time"
)

// Kind 表示记忆的类型。
type Kind string

const (
	KindFact       Kind = "fact"       // 事实陈述：用户告诉 Agent 的客观信息
	KindPreference Kind = "preference" // 偏好：用户的喜好、习惯、约定
	KindEvent      Kind = "event"      // 事件：发生过的经历、操作记录
	KindKnowledge  Kind = "knowledge"  // 知识：领域知识、结论、经验总结
	KindProcedure  Kind = "procedure"  // 流程：操作步骤、工作方法
)

// memoryKindSet 用于快速校验传入的 Kind 是否合法。
var memoryKindSet = map[Kind]bool{
	KindFact: true, KindPreference: true, KindEvent: true,
	KindKnowledge: true, KindProcedure: true,
}

// NormalizeKind 归一化记忆类型：空值返回默认 KindFact，
// 大小写不敏感；无法识别的类型返回错误。
func NormalizeKind(s string) (Kind, error) {
	if strings.TrimSpace(s) == "" {
		return KindFact, nil
	}
	k := Kind(strings.ToLower(strings.TrimSpace(s)))
	if !memoryKindSet[k] {
		return "", fmt.Errorf("未知的记忆类型 %q（可选: fact, preference, event, knowledge, procedure）", s)
	}
	return k, nil
}

// 记忆在 govector payload 中的字段名。
const (
	payloadID         = "id"
	payloadContent    = "content"
	payloadKind       = "kind"
	payloadTags       = "tags"
	payloadImportance = "importance"
	payloadScope      = "scope"
	payloadCreatedAt  = "created_at"
	payloadUpdatedAt  = "updated_at"
	payloadAccessCnt  = "access_count"
)

// DefaultScope 是未指定 scope 时使用的默认命名空间。
const DefaultScope = "default"

// DefaultImportance 是未指定重要度时使用的默认值。
const DefaultImportance = 0.5

// Memory 是一条结构化记忆。
type Memory struct {
	ID          string    `json:"id"`                   // 唯一标识（mem_ 前缀）
	Content     string    `json:"content"`              // 记忆正文
	Kind        Kind      `json:"kind"`                 // 记忆类型
	Tags        []string  `json:"tags,omitempty"`       // 自由标签，用于精确过滤
	Importance  float64   `json:"importance"`           // 重要度 [0,1]，参与检索排序
	Scope       string    `json:"scope"`                // 命名空间（如用户/项目 ID）
	Score       float64   `json:"score,omitempty"`      // 检索综合得分（仅检索结果携带）
	Similarity  float64   `json:"similarity,omitempty"` // 原始向量相似度（仅检索结果携带）
	CreatedAt   time.Time `json:"created_at"`           // 首次创建时间
	UpdatedAt   time.Time `json:"updated_at"`           // 最近一次更新/强化时间
	AccessCount int       `json:"access_count"`         // 被重复记忆（合并强化）的次数
}

// ClampImportance 将重要度约束到 [0,1] 区间，0 值视为未设置并返回默认值。
func ClampImportance(v float64) float64 {
	if v == 0 {
		return DefaultImportance
	}
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
