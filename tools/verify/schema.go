package verify

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// schemaLeaf 是 Schema 里的一个字段路径。
//
// Parent 记录直接父路径：属性名本身可以含点（backend.hooks.turn.before），
// 所以不能靠字符串前缀判定父子关系。
type schemaLeaf struct {
	Path     string
	Parent   string
	Consumer string
	Type     string
	Enum     []string
}

// schemaInfo 是一次遍历的结果。
type schemaInfo struct {
	Leaves []schemaLeaf
	ByPath map[string]schemaLeaf
	// RootArrays 收集根级 x-* 字符串数组（白名单）。
	RootArrays map[string][]string
	// RootNumbers 收集根级 x-* 数值与 x-limit 对象。
	RootNumbers map[string]float64
}

// loadSchema 读取并遍历一个 JSON Schema 文件。
func loadSchema(path string) (*schemaInfo, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("verify: 读取 Schema 失败 %s: %w", path, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("verify: 解析 Schema 失败 %s: %w", path, err)
	}
	info := &schemaInfo{
		ByPath:      map[string]schemaLeaf{},
		RootArrays:  map[string][]string{},
		RootNumbers: map[string]float64{},
	}
	for k, v := range doc {
		if !strings.HasPrefix(k, "x-") {
			continue
		}
		switch t := v.(type) {
		case []any:
			if strs := toStrings(t); strs != nil {
				info.RootArrays[strings.TrimPrefix(k, "x-")] = strs
			}
		case map[string]any:
			for sub, sv := range t {
				if f, ok := toNumber(sv); ok {
					info.RootNumbers[k+"/"+sub] = f
				}
			}
		case float64:
			info.RootNumbers[strings.TrimPrefix(k, "x-")] = t
		}
	}
	if err := walkSchema("", doc, info, false, ""); err != nil {
		return nil, err
	}
	// $defs 里的结构本身不是协议字段，只登记它的子字段（路径以定义名开头，例如 element.t）。
	if defs, ok := doc["$defs"].(map[string]any); ok {
		names := make([]string, 0, len(defs))
		for name := range defs {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			node, ok := defs[name].(map[string]any)
			if !ok {
				continue
			}
			if err := walkSchema(name, node, info, false, ""); err != nil {
				return nil, err
			}
		}
	}
	sort.Slice(info.Leaves, func(i, j int) bool { return info.Leaves[i].Path < info.Leaves[j].Path })
	return info, nil
}

// walkSchema 递归 properties / items / additionalProperties / patternProperties。
// 路径不产生 [] 或 {} 标记，与 Go 结构侧同一口径；recordSelf=false 时只登记子字段，
// 不把「结构包装」当字段（否则 requireConsumers 会把 $defs 误报成缺消费者）。
func walkSchema(path string, node map[string]any, info *schemaInfo, recordSelf bool, parent string) error {
	consumer, _ := node["x-consumer"].(string)
	typ := schemaType(node)
	enum := toStrings(anySlice(node["enum"]))

	if path != "" {
		leaf := schemaLeaf{Path: path, Parent: parent, Consumer: consumer, Type: typ, Enum: enum}
		if recordSelf {
			info.Leaves = append(info.Leaves, leaf)
			info.ByPath[path] = leaf
		} else if existing, ok := info.ByPath[path]; ok {
			// items / additionalProperties 与容器同路径：把枚举与消费者合并进已有条目，
			// 这样 backend.capabilities 的 items.enum 能被 enumOf 查到。
			if len(existing.Enum) == 0 && len(enum) > 0 {
				existing.Enum = enum
			}
			if existing.Consumer == "" && consumer != "" {
				existing.Consumer = consumer
			}
			if existing.Type == "" || strings.HasPrefix(existing.Type, "const:") {
				existing.Type = typ
			}
			info.ByPath[path] = existing
		} else {
			info.ByPath[path] = leaf // 可被 enumOf 查到，但不计入字段清单
		}
	}

	if props, ok := node["properties"].(map[string]any); ok {
		for name, sub := range props {
			child, ok := sub.(map[string]any)
			if !ok {
				return fmt.Errorf("verify: Schema 路径 %s.%s 的子节点不是对象", displayPath(path), name)
			}
			next := name
			if path != "" {
				next = path + "." + name
			}
			if err := walkSchema(next, child, info, true, path); err != nil {
				return err
			}
		}
	}
	// allOf / if-then 里也可能声明 properties。
	for _, key := range []string{"allOf", "anyOf", "oneOf"} {
		for _, item := range anySlice(node[key]) {
			if m, ok := item.(map[string]any); ok {
				if err := walkContainer(path, m, info); err != nil {
					return err
				}
			}
		}
	}
	for _, key := range []string{"if", "then", "else"} {
		if m, ok := node[key].(map[string]any); ok {
			if err := walkContainer(path, m, info); err != nil {
				return err
			}
		}
	}

	if items, ok := node["items"].(map[string]any); ok {
		if err := walkSchema(path, items, info, false, ""); err != nil {
			return err
		}
	}
	if ap, ok := node["additionalProperties"].(map[string]any); ok {
		if err := walkSchema(path, ap, info, false, ""); err != nil {
			return err
		}
	}
	if pp, ok := node["patternProperties"].(map[string]any); ok {
		keys := make([]string, 0, len(pp))
		for k := range pp {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if m, ok := pp[k].(map[string]any); ok {
				if err := walkSchema(path, m, info, false, ""); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// walkContainer 处理组合子句里的 properties，不新增路径（避免把条件子句当成字段）。
func walkContainer(prefix string, node map[string]any, info *schemaInfo) error {
	props, ok := node["properties"].(map[string]any)
	if !ok {
		return nil
	}
	for name, sub := range props {
		m, ok := sub.(map[string]any)
		if !ok {
			continue
		}
		next := name
		if prefix != "" {
			next = prefix + "." + name
		}
		if _, exists := info.ByPath[next]; exists {
			// 条件子句重复声明同一字段：把它的消费者合并过来即可。
			if c, ok := m["x-consumer"].(string); ok && c != "" {
				l := info.ByPath[next]
				if l.Consumer == "" {
					l.Consumer = c
					info.ByPath[next] = l
				}
			}
			continue
		}
		if err := walkSchema(next, m, info, true, prefix); err != nil {
			return err
		}
	}
	return nil
}

func displayPath(p string) string {
	if p == "" {
		return "<root>"
	}
	return p
}

func schemaType(node map[string]any) string {
	switch t := node["type"].(type) {
	case string:
		return t
	case []any:
		parts := toStrings(t)
		if len(parts) > 0 {
			return strings.Join(parts, "|")
		}
	}
	if _, ok := node["enum"]; ok {
		return "string"
	}
	return ""
}

func anySlice(v any) []any {
	if s, ok := v.([]any); ok {
		return s
	}
	return nil
}

func toStrings(xs []any) []string {
	if len(xs) == 0 {
		return nil
	}
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		s, ok := x.(string)
		if !ok {
			return nil
		}
		out = append(out, s)
	}
	return out
}

func toNumber(v any) (float64, bool) {
	if f, ok := v.(float64); ok {
		return f, true
	}
	return 0, false
}

// requireConsumers 断言每个字段都有消费者说明。
func (s *schemaInfo) requireConsumers(file string) error {
	var missing []string
	for _, l := range s.Leaves {
		if strings.TrimSpace(l.Consumer) == "" {
			missing = append(missing, l.Path)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("verify: %s 有 %d 个字段没有 x-consumer：%s",
			file, len(missing), strings.Join(missing, ", "))
	}
	return nil
}

// enumOf 取某路径的枚举值（包含 items / additionalProperties 合并进来的枚举）。
func (s *schemaInfo) enumOf(path string) []string {
	if l, ok := s.ByPath[path]; ok {
		return l.Enum
	}
	return nil
}

// paths 返回登记为「字段」的路径清单（$defs 与容器包装不在其中）。
func (s *schemaInfo) paths() []string {
	out := make([]string, 0, len(s.Leaves))
	for _, l := range s.Leaves {
		out = append(out, l.Path)
	}
	sort.Strings(out)
	return out
}

// setDiff 返回 a 有 b 没有 / b 有 a 没有。
func setDiff(a, b []string) (onlyA, onlyB []string) {
	inA := map[string]bool{}
	inB := map[string]bool{}
	for _, x := range a {
		inA[x] = true
	}
	for _, x := range b {
		inB[x] = true
	}
	for _, x := range a {
		if !inB[x] {
			onlyA = append(onlyA, x)
		}
	}
	for _, x := range b {
		if !inA[x] {
			onlyB = append(onlyB, x)
		}
	}
	sort.Strings(onlyA)
	sort.Strings(onlyB)
	return onlyA, onlyB
}
