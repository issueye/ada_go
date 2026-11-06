package design

import (
	"fmt"
	"testing"
)

// TestSpecProbePrint 打印各体系规范违例（临时探针，也可保留作为文档输出）。
func TestSpecProbePrint(t *testing.T) {
	for _, s := range Systems() {
		for _, theme := range s.Themes {
			issues := SpecConformance(s, ThemeKey(theme))
			fmt.Printf("%s/%s: %d 处违例 -> ", s.ID, theme, len(issues))
			for i, is := range issues {
				if i >= 10 {
					fmt.Print("...")
					break
				}
				fmt.Printf("[%s %s %v→%d] ", is.Prop, is.Kind, is.Value, is.Nearest)
			}
			fmt.Println()
		}
	}
}
