package corerpc

import (
	"strconv"
	"testing"
	"time"
)

func TestPruneIdemLocked(t *testing.T) {
	now := time.Now().UTC()
	m := map[string]idemEntry{}

	// 低于扫描阈值：即使全部过期也不清理（避免低频场景白扫）。
	m["a"] = idemEntry{runID: "run_a", at: now.Add(-2 * idemRetention)}
	pruneIdemLocked(m, now)
	if len(m) != 1 {
		t.Fatalf("低于阈值时不应清理，得到 %d 项", len(m))
	}

	// 超过阈值：过期项清掉，未过期保留。
	for i := 0; i < 600; i++ {
		m["k"+strconv.Itoa(i)] = idemEntry{runID: "r", at: now.Add(-2 * idemRetention)}
	}
	m["fresh"] = idemEntry{runID: "run_fresh", at: now}
	pruneIdemLocked(m, now)
	if _, ok := m["fresh"]; !ok {
		t.Fatal("未过期项不应被清理")
	}
	if len(m) != 1 {
		t.Fatalf("过期项应全部清理，剩 %d 项", len(m))
	}
}
