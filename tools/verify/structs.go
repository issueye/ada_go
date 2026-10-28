package verify

import (
	"reflect"
	"sort"
	"strings"
)

// structShape 是 Go 结构的字段路径视图，口径与 Schema 对齐：
//   - 数组/map 的元素字段挂在同一路径下（Schema 的 items / additionalProperties 也是这样）；
//   - map 字段额外记录进 Maps：它吸收 Schema 里该路径下的所有子字段
//     （例如 backend.hooks 在 Schema 里逐点枚举 15 个点位，Go 侧是 map[string]HookAction）。
type structShape struct {
	Paths []string
	Maps  []string
}

// structPaths 递归列出一个类型的所有 json 字段路径。
func structPaths(t reflect.Type) structShape {
	s := structShape{}
	walkType("", t, &s)
	sort.Strings(s.Paths)
	sort.Strings(s.Maps)
	return s
}

func walkType(prefix string, t reflect.Type, s *structShape) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil || t.Kind() == reflect.Invalid {
		return
	}
	// json.RawMessage 与 time.* 是自由格式/标量叶子，不再下钻。
	if t.PkgPath() == "encoding/json" && t.Name() == "RawMessage" {
		return
	}
	if isTimeLike(t) {
		return
	}
	switch t.Kind() {
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if f.PkgPath != "" { // 非导出字段不参与协议
				continue
			}
			name, ok := jsonName(f)
			if !ok {
				continue
			}
			path := name
			if prefix != "" {
				path = prefix + "." + name
			}
			s.Paths = append(s.Paths, path)
			if f.Type.Kind() == reflect.Map {
				s.Maps = append(s.Maps, path)
			}
			walkType(path, f.Type, s)
		}
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return // []byte / []uint8 视为叶子
		}
		walkType(prefix, t.Elem(), s)
	case reflect.Map:
		if t.Elem().Kind() != reflect.Struct {
			return // map[string]string 之类的标量值不再下钻
		}
		walkType(prefix, t.Elem(), s)
	}
}

// jsonName 解析 struct tag；`json:"-"` 不算协议字段。
func jsonName(f reflect.StructField) (string, bool) {
	tag, ok := f.Tag.Lookup("json")
	if !ok {
		return "", false
	}
	name, _, _ := strings.Cut(tag, ",")
	if name == "" {
		name = f.Name
	}
	if name == "-" {
		return "", false
	}
	return name, true
}

func isTimeLike(t reflect.Type) bool {
	return t.PkgPath() == "time" && (t.Name() == "Time" || t.Name() == "Duration")
}

// absorbSchemaMapChildren 剔除位于某个 map 字段之下的 Schema 路径
// （backend.hooks / frontend.actions 这类：Go 用 map，Schema 可能逐点枚举）。
func absorbSchemaMapChildren(info *schemaInfo, paths []string, maps []string) []string {
	isMap := make(map[string]bool, len(maps))
	for _, m := range maps {
		isMap[m] = true
	}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		cur, depth := p, 0
		absorbed := false
		for depth < 16 {
			depth++
			l, ok := info.ByPath[cur]
			if !ok || l.Parent == "" {
				break
			}
			if isMap[l.Parent] {
				absorbed = true
				break
			}
			cur = l.Parent
		}
		if !absorbed {
			out = append(out, p)
		}
	}
	return out
}

// absorbGoMapChildren 对 Go 结构路径做同样的吸收（Go 的 json 名不含点，按最后一段裁剪）。
func absorbGoMapChildren(paths []string, maps []string) []string {
	isMap := make(map[string]bool, len(maps))
	for _, m := range maps {
		isMap[m] = true
	}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		absorbed := false
		for cur := p; cur != ""; {
			i := strings.LastIndex(cur, ".")
			if i < 0 {
				break
			}
			parent := cur[:i]
			if isMap[parent] {
				absorbed = true
				break
			}
			cur = parent
		}
		if !absorbed {
			out = append(out, p)
		}
	}
	return out
}
