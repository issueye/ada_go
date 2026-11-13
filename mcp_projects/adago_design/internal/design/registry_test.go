package design

import (
	"sync"
	"testing"
)

// TestRegistryConcurrentAccess 守住注册表的并发安全：MCP 工具在运行期
// 注册/删除自定义体系时，画布目录接口可能正在读取（-race 下必须干净）。
func TestRegistryConcurrentAccess(t *testing.T) {
	const id = "race-probe"
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = Systems()
					_, _ = GetSystem("apple")
				}
			}
		}()
	}
	for i := 0; i < 200; i++ {
		RegisterOrReplace(&System{ID: id, Name: "race"})
		unregisterSystem(id)
	}
	close(stop)
	wg.Wait()
	if _, ok := GetSystem(id); ok {
		t.Fatalf("探针体系 %s 未清理", id)
	}
}

// TestDeriveDoesNotOverwriteSameName 同名同主色重复派生应得到递增 ID，
// 而不是静默覆盖前一个自定义体系。
func TestDeriveDoesNotOverwriteSameName(t *testing.T) {
	dir := t.TempDir()
	in := DeriveInput{Primary: "#C8412B", Name: "Registry Dedup Probe", BaseSystemID: "volcano"}
	first, err := Derive(in)
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveCustomSystem(first, dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = RemoveCustomSystem(first.ID, dir) })

	second, err := Derive(in)
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveCustomSystem(second, dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = RemoveCustomSystem(second.ID, dir) })

	if first.ID == second.ID {
		t.Fatalf("重复派生得到了相同 ID %q", first.ID)
	}
	for _, want := range []*System{first, second} {
		got, ok := GetSystem(want.ID)
		if !ok || got.ID != want.ID {
			t.Fatalf("体系 %q 未保留（got=%v ok=%v）", want.ID, got, ok)
		}
	}
}
