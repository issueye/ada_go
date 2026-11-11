// 内置设计体系数据的装载（迭代 3-⑤）。
//
// 体系的单一真源是 data/<id>.json（8 套内置体系），Go 源码不再内联 Token
// 数据 —— 数据与代码分离，新增/修订体系改 JSON 即可，不变量（颜色合法 hex、
// 字体可商用、组件可渲染、规格阶梯）仍由本包的门禁测试把关。
// 由 gen_export 从旧 system_*.go 一次性迁出；社区体系包可用同一结构提交。
package design

import (
	"embed"
	"encoding/json"
	"fmt"
	"log"
	"sort"
)

//go:embed data/*.json
var systemDataFS embed.FS

func init() {
	entries, err := systemDataFS.ReadDir("data")
	if err != nil {
		panic(fmt.Sprintf("design: 读取体系数据目录失败: %v", err))
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || nameExt(e.Name()) != ".json" {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		raw, err := systemDataFS.ReadFile("data/" + name)
		if err != nil {
			panic(fmt.Sprintf("design: 读取 %s: %v", name, err))
		}
		var sys System
		if err := json.Unmarshal(raw, &sys); err != nil {
			panic(fmt.Sprintf("design: 解析 %s: %v", name, err))
		}
		if sys.ID == "" {
			panic(fmt.Sprintf("design: %s 缺少 id 字段", name))
		}
		Register(&sys)
	}
	if len(registry) == 0 {
		log.Println("[design] 警告：未装载任何内置体系")
	}
}

func nameExt(name string) string {
	for i := len(name) - 1; i >= 0; i-- {
		if name[i] == '.' {
			return name[i:]
		}
	}
	return ""
}
