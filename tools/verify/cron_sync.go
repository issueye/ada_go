package verify

// cron-sync 检查：定时任务的跨进程契约文件是「双写镜像」——主模块与独立 cron
// 二进制各持一份几乎相同的源码，历史上只靠注释互指约定同步，且已实际漂移过
// （二进制侧缺任务名控制字符校验）。本检查把镜像约束变成门禁：
//
//   - internal/cronstore/cronexpr.go ↔ mcp_projects/cron/internal/cronexpr/cronexpr.go
//     除 package 行外逐字节一致；
//   - internal/cronstore/cronstore.go ↔ mcp_projects/cron/internal/store/store.go
//     除 package 行、import 块与 cronexpr 限定名外逐字节一致。
//
// 任一侧改动都必须同步另一侧；检查不过时按报出的首个差异行对照修改。

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func checkCronSync(root string) error {
	exprMain := filepath.Join(root, "internal", "cronstore", "cronexpr.go")
	exprBin := filepath.Join(root, "mcp_projects", "cron", "internal", "cronexpr", "cronexpr.go")
	if err := compareMirror(exprMain, exprBin, stripPackageLine); err != nil {
		return fmt.Errorf("cronexpr 镜像漂移: %w", err)
	}
	storeMain := filepath.Join(root, "internal", "cronstore", "cronstore.go")
	storeBin := filepath.Join(root, "mcp_projects", "cron", "internal", "store", "store.go")
	if err := compareMirror(storeMain, storeBin, stripStoreMirror); err != nil {
		return fmt.Errorf("store 镜像漂移: %w", err)
	}
	return nil
}

// stripStoreMirror 归一化 store 侧镜像：去 package 行、去 import 块、去 cronexpr 限定名。
func stripStoreMirror(src string) (string, error) {
	s, err := stripPackageLine(src)
	if err != nil {
		return "", err
	}
	s = stripImportBlock(s)
	return strings.ReplaceAll(s, "cronexpr.", ""), nil
}

// compareMirror 对两个文件各自 normalize 后逐字节比较；不一致时报出首个差异行，
// 方便对照修改。
func compareMirror(aPath, bPath string, normalize func(string) (string, error)) error {
	aRaw, err := os.ReadFile(aPath)
	if err != nil {
		return err
	}
	bRaw, err := os.ReadFile(bPath)
	if err != nil {
		return err
	}
	a, err := normalize(string(aRaw))
	if err != nil {
		return fmt.Errorf("%s: %w", aPath, err)
	}
	b, err := normalize(string(bRaw))
	if err != nil {
		return fmt.Errorf("%s: %w", bPath, err)
	}
	if a == b {
		return nil
	}
	alines := strings.Split(a, "\n")
	blines := strings.Split(b, "\n")
	for i := 0; i < len(alines) || i < len(blines); i++ {
		al, bl := "", ""
		if i < len(alines) {
			al = alines[i]
		}
		if i < len(blines) {
			bl = blines[i]
		}
		if al != bl {
			return fmt.Errorf("%s 与 %s 规范化后在第 %d 行不一致:\n  A: %s\n  B: %s", aPath, bPath, i+1, al, bl)
		}
	}
	return fmt.Errorf("%s 与 %s 规范化后不一致", aPath, bPath)
}

// stripPackageLine 去掉 package 声明行（保留其余行序与空行，两侧结构平行）。
func stripPackageLine(src string) (string, error) {
	lines := strings.Split(src, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "package ") {
			out := make([]string, 0, len(lines)-1)
			out = append(out, lines[:i]...)
			out = append(out, lines[i+1:]...)
			return strings.Join(out, "\n"), nil
		}
	}
	return "", fmt.Errorf("没有 package 行")
}

// stripImportBlock 去掉 import 声明（块形式 `import ( ... )` 或单行 `import "x"`）。
func stripImportBlock(src string) string {
	lines := strings.Split(src, "\n")
	out := make([]string, 0, len(lines))
	for i := 0; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if strings.HasPrefix(trimmed, "import (") {
			for i < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i]), ")") {
				i++
			}
			continue
		}
		if strings.HasPrefix(trimmed, "import ") && strings.Contains(trimmed, "\"") {
			continue
		}
		out = append(out, lines[i])
	}
	return strings.Join(out, "\n")
}
