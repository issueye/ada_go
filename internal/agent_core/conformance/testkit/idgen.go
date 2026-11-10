package testkit

import (
	"context"
	"fmt"
	"regexp"
	"sync"
	"time"
)

// ULID 形状：26 位 Crockford 大写 base32（前 10 位是毫秒时间戳，后 16 位是单调尾）。
var ulidPattern = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)

// ThreadIDPattern 是线程 id 的冻结形状（01 §3.1、08 生成常量 proto.ThreadIDFormat）。
var ThreadIDPattern = regexp.MustCompile(`^th_[0-9a-z]{4,32}_[0-9A-HJKMNP-TV-Z]{26}$`)

// CallIDPattern / CheckpointIDPattern 是 08 §8.4 回执里 call_ / chk_ 前缀的形状。
var (
	CallIDPattern       = regexp.MustCompile(`^call_[0-9A-HJKMNP-TV-Z]{26}$`)
	CheckpointIDPattern = regexp.MustCompile(`^chk_[0-9A-HJKMNP-TV-Z]{26}$`)
)

// FixedIDGen 是 ports.IDGen 的测试替身：确定、可预测、严格单调（可排序）。
//
// 守的不变量：
//   - INV-6（02 §5.3）：事件 golden 只有在 id 固定时才可比对；
//   - INV-4/03 §5.6：交互幂等键是 (threadID, requestID)，只有 id 稳定才测得出
//     「重复响应返回首次结果」。
//
// 形状全部照 ports/idgen.go 的注释：th_<workspaceHash>_<ulid>、call_、chk_、裸 ULID。
// 每个类别有独立计数器，因此同一序号在不同前缀下会得到不同的尾段，
// 便于在断言里区分「哪个 id」。
type FixedIDGen struct {
	mu     sync.Mutex
	byKind map[string]int64
	// Base 是裸 ULID 时间段的起点（默认 FrozenTime）。
	Base time.Time
}

// NewFixedIDGen 构造确定性 id 源。
func NewFixedIDGen() *FixedIDGen {
	return &FixedIDGen{byKind: make(map[string]int64), Base: FrozenTime}
}

// NextULID 返回裸 ULID 形状字符串（供外部拼装非端口 id，例如检查点内的文件指纹）。
func (g *FixedIDGen) NextULID(kind string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.byKind == nil {
		g.byKind = make(map[string]int64)
	}
	g.byKind[kind]++
	n := g.byKind[kind]
	base := g.Base
	if base.IsZero() {
		base = FrozenTime
	}
	// 时间段按类别推进 1ms，使不同类别的 id 也彼此可排序。
	ms := base.UnixMilli() + int64(len(kind))
	return encodeULID(ms, uint64(n))
}

// NewThreadID 生成 th_<workspaceHash>_<ulid>。
func (g *FixedIDGen) NewThreadID(_ context.Context, workspaceHash string) (string, error) {
	if workspaceHash == "" {
		return "", fmt.Errorf("testkit: NewThreadID 需要非空 workspaceHash")
	}
	return fmt.Sprintf("th_%s_%s", workspaceHash, g.NextULID("thread")), nil
}

// NewULID 生成裸 ULID（entry id 等，04 §5.7）。
func (g *FixedIDGen) NewULID(_ context.Context) (string, error) { return g.NextULID("entry"), nil }

// NewCallID 生成工具调用 id（call_ 前缀）。
func (g *FixedIDGen) NewCallID(_ context.Context) (string, error) {
	return "call_" + g.NextULID("call"), nil
}

// NewRunID 生成轮次 id（runs.id，裸 ULID）。
func (g *FixedIDGen) NewRunID(_ context.Context) (string, error) { return g.NextULID("run"), nil }

// NewCheckpointID 生成检查点 id（chk_ 前缀）。
func (g *FixedIDGen) NewCheckpointID(_ context.Context) (string, error) {
	return "chk_" + g.NextULID("chk"), nil
}

// NewMessageID 生成投影消息 id（evt.message.delta 的 messageId）。
// 协议未冻结它的前缀（见 M0.5 汇报的契约缺口），这里返回裸 ULID。
func (g *FixedIDGen) NewMessageID(_ context.Context) (string, error) {
	return g.NextULID("msg"), nil
}

// NewInteractionID 生成交互请求 id（interactions.id）。
// 同样未冻结前缀，返回裸 ULID。
func (g *FixedIDGen) NewInteractionID(_ context.Context) (string, error) {
	return g.NextULID("inter"), nil
}

// Reset 把计数器清零（在同一测试里重放同一段脚本时使用）。
func (g *FixedIDGen) Reset() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.byKind = make(map[string]int64)
}

// IsULID 报告 s 是否是合法 ULID 形状（golden 断言与 idgen 自检共用）。
func IsULID(s string) bool { return ulidPattern.MatchString(s) }

// encodeULID 把 48 位毫秒时间 + 80 位尾数编码成 26 位 Crockford base32。
// 只保证形状与单调性（熵段用计数器填充），不保证与 oklog/ulid 生成的值相同。
func encodeULID(ms int64, counter uint64) string {
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	var raw [16]byte
	_ = raw[15]
	for i := 0; i < 6; i++ {
		raw[i] = byte(ms >> uint(40-8*i))
	}
	// 熵段（10 字节）：计数器放在最后 8 字节，前面留 0，形状合法且单调。
	for i := 0; i < 8; i++ {
		raw[6+2+i] = byte(counter >> uint(56-8*i))
	}
	out := make([]byte, 26)
	// 130 bit 输出：首组只有 3 个有效 bit（ULID 规范要求首位 ≤ 7）。
	for i := 0; i < 26; i++ {
		var v uint32
		bit := i * 5
		for j := 0; j < 5; j++ {
			pos := bit + j - 2 // 相对 128 bit 数据的位偏移
			var b uint32
			if pos >= 0 && pos < 128 {
				byteIdx := pos / 8
				bitIdx := uint(7 - pos%8)
				b = uint32(raw[byteIdx]>>bitIdx) & 1
			}
			v = v<<1 | b
		}
		out[i] = alphabet[v]
	}
	return string(out)
}
