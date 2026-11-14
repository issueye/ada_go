// gen_export 把当前注册的设计体系导出为 data/<id>.json —— 体系数据的
// 单一真源在这些 JSON（go:embed 装载），Go 源码不再内联 Token 数据。
//
// 用途：
//   - 首次从 system_*.go 迁移时生成数据文件；
//   - 校验手工编辑后的 JSON 仍能被结构体完整承接（export 后 go test）；
//   - 批量调整数据后回写（改 JSON → go run 本工具无意义，改数据应直接编辑 JSON）。
//
// 用法：GOWORK=off go run ./internal/design/gen_export -out internal/design/data
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"adagodesign/internal/design"
)

func main() {
	out := flag.String("out", "", "输出目录（默认 internal/design/data）")
	flag.Parse()
	dir := *out
	if dir == "" {
		dir = "internal/design/data"
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "create dir: %v\n", err)
		os.Exit(1)
	}
	systems := design.Systems()
	if len(systems) == 0 {
		fmt.Fprintln(os.Stderr, "no systems registered")
		os.Exit(1)
	}
	for _, sys := range systems {
		raw, err := json.MarshalIndent(sys, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "marshal %s: %v\n", sys.ID, err)
			os.Exit(1)
		}
		file := filepath.Join(dir, sys.ID+".json")
		if err := os.WriteFile(file, raw, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "write %s: %v\n", file, err)
			os.Exit(1)
		}
		fmt.Println(file)
	}
	names := make([]string, 0, len(systems))
	for _, sys := range systems {
		names = append(names, sys.ID)
	}
	sort.Strings(names)
	fmt.Printf("%d systems exported\n", len(names))
}
