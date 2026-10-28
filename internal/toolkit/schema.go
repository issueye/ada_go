package toolkit

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
)

// 参数校验器：JSON Schema draft 2020-12 的一个封闭子集。
//
// 为什么不用现成库：工具参数直接决定「改哪个文件、跑哪条命令」，校验被静默跳过就等于
// 没有校验（02 §5.4：校验失败必须直接生成 error 回执）。因此这里只支持白名单关键字，
// 出现未支持的关键字时在装配期就拒收描述符，而不是运行期当作「没有约束」。

// supportedKeywords 是允许出现在工具 parameters Schema 里的关键字。
var supportedKeywords = map[string]bool{
	"type": true, "properties": true, "required": true, "additionalProperties": true,
	"items": true, "enum": true, "minimum": true, "maximum": true,
	"minLength": true, "maxLength": true, "pattern": true,
	"minItems": true, "maxItems": true, "uniqueItems": true,
	"description": true, "default": true, "format": true, "title": true,
	"nullable": true, "$comment": true,
}

type schema struct {
	Type       string
	Properties map[string]*schema
	Required   []string
	// AdditionalProperties: nil=未声明(允许)，非 nil=受限 Schema 或布尔。
	AdditionalAny *bool
	Items         *schema
	Enum          []any
	Minimum       *float64
	Maximum       *float64
	MinLength     *int
	MaxLength     *int
	Pattern       *regexp.Regexp
	MinItems      *int
	MaxItems      *int
	UniqueItems   bool
	DeclaredType  []string
	// itemsSchema 是 additionalProperties 为对象 Schema 时的通用约束。
	itemsSchema *schema
}

// compileSchema 解析并静态校验 Schema（装配期调用）。
func compileSchema(raw json.RawMessage) (*schema, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("toolkit: parameters 为空")
	}
	var node map[string]any
	if err := json.Unmarshal(raw, &node); err != nil {
		return nil, fmt.Errorf("toolkit: parameters 不是合法 JSON 对象: %w", err)
	}
	return compileNode(node, "")
}

func compileNode(node map[string]any, path string) (*schema, error) {
	s := &schema{}
	for k := range node {
		if !supportedKeywords[k] {
			return nil, fmt.Errorf("toolkit: Schema %s 含未支持的关键字 %q；v1 只支持封闭子集，"+
				"未支持的约束会被当成无约束，因此拒收而不是忽略", displayNode(path), k)
		}
	}
	if t, ok := node["type"]; ok {
		switch v := t.(type) {
		case string:
			s.Type = v
		case []any:
			names, err := toStringList(v)
			if err != nil {
				return nil, fmt.Errorf("toolkit: Schema %s 的 type 数组非法: %w", displayNode(path), err)
			}
			s.DeclaredType = names
			if len(names) == 1 {
				s.Type = names[0]
			}
		default:
			return nil, fmt.Errorf("toolkit: Schema %s 的 type 必须是字符串或字符串数组", displayNode(path))
		}
	}
	if props, ok := node["properties"].(map[string]any); ok {
		s.Properties = map[string]*schema{}
		for name, sub := range props {
			child, ok := sub.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("toolkit: Schema %s.%s 不是对象", displayNode(path), name)
			}
			cs, err := compileNode(child, joinNode(path, name))
			if err != nil {
				return nil, err
			}
			s.Properties[name] = cs
		}
	}
	if req, ok := node["required"]; ok {
		l, err := toStringList(anySlice(req))
		if err != nil {
			return nil, fmt.Errorf("toolkit: Schema %s 的 required 非法: %w", displayNode(path), err)
		}
		s.Required = l
	}
	switch ap := node["additionalProperties"].(type) {
	case bool:
		v := ap
		s.AdditionalAny = &v
	case map[string]any:
		cs, err := compileNode(ap, joinNode(path, "[]"))
		if err != nil {
			return nil, err
		}
		if cs != nil {
			v := true
			s.AdditionalAny = &v
			// 子 Schema 继续约束额外属性：把它并进 Properties 之外的通用校验。
			s.itemsSchema = cs
		}
	case nil:
	default:
		return nil, fmt.Errorf("toolkit: Schema %s 的 additionalProperties 只能是布尔或对象", displayNode(path))
	}
	if items, ok := node["items"]; ok {
		switch v := items.(type) {
		case map[string]any:
			cs, err := compileNode(v, joinNode(path, "[]"))
			if err != nil {
				return nil, err
			}
			s.Items = cs
		default:
			return nil, fmt.Errorf("toolkit: Schema %s 的 items 只支持单一 Schema（不支持元组）", displayNode(path))
		}
	}
	if enum, ok := node["enum"]; ok {
		s.Enum = anySlice(enum)
	}
	if f, ok := number(node["minimum"]); ok {
		v := f
		s.Minimum = &v
	}
	if f, ok := number(node["maximum"]); ok {
		v := f
		s.Maximum = &v
	}
	if f, ok := number(node["minLength"]); ok {
		v := int(f)
		s.MinLength = &v
	}
	if f, ok := number(node["maxLength"]); ok {
		v := int(f)
		s.MaxLength = &v
	}
	if p, ok := node["pattern"].(string); ok {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("toolkit: Schema %s 的 pattern %q 无法编译: %w", displayNode(path), p, err)
		}
		s.Pattern = re
	}
	if f, ok := number(node["minItems"]); ok {
		v := int(f)
		s.MinItems = &v
	}
	if f, ok := number(node["maxItems"]); ok {
		v := int(f)
		s.MaxItems = &v
	}
	if b, ok := node["uniqueItems"].(bool); ok {
		s.UniqueItems = b
	}
	return s, nil
}

// Validate 校验参数原文；错误信息带 JSON 路径，直接进 error 回执给模型自我修正（02 §5.4）。
func (s *schema) Validate(raw json.RawMessage) error {
	if s == nil {
		return fmt.Errorf("toolkit: 空 Schema")
	}
	var v any
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if len(raw) == 0 {
		v = nil
	} else if err := dec.Decode(&v); err != nil {
		return fmt.Errorf("toolkit: 参数不是合法 JSON: %w", err)
	}
	return s.check("$", v)
}

func (s *schema) check(path string, v any) error {
	if err := s.checkType(path, v); err != nil {
		return err
	}
	if len(s.Enum) > 0 && !enumHas(s.Enum, v) {
		return fmt.Errorf("toolkit: %s 的值 %v 不在 enum 里", path, v)
	}
	switch val := v.(type) {
	case map[string]any:
		if err := s.checkObject(path, val); err != nil {
			return err
		}
	case []any:
		if err := s.checkArray(path, val); err != nil {
			return err
		}
	case string:
		if s.MinLength != nil && len([]rune(val)) < *s.MinLength {
			return fmt.Errorf("toolkit: %s 长度 %d 小于 minLength %d", path, len([]rune(val)), *s.MinLength)
		}
		if s.MaxLength != nil && len([]rune(val)) > *s.MaxLength {
			return fmt.Errorf("toolkit: %s 长度 %d 大于 maxLength %d", path, len([]rune(val)), *s.MaxLength)
		}
		if s.Pattern != nil && !s.Pattern.MatchString(val) {
			return fmt.Errorf("toolkit: %s 不匹配 pattern %q", path, s.Pattern.String())
		}
	}
	return nil
}

func (s *schema) checkObject(path string, obj map[string]any) error {
	for _, name := range s.Required {
		if _, ok := obj[name]; !ok {
			return fmt.Errorf("toolkit: %s 缺少必填字段 %q", path, name)
		}
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		sub, ok := s.Properties[k]
		if !ok {
			if s.AdditionalAny != nil && !*s.AdditionalAny {
				return fmt.Errorf("toolkit: %s 含未声明的额外字段 %q（additionalProperties=false）", path, k)
			}
			if s.itemsSchema != nil {
				if err := s.itemsSchema.check(joinNode(path, k), obj[k]); err != nil {
					return err
				}
			}
			continue
		}
		if err := sub.check(joinNode(path, k), obj[k]); err != nil {
			return err
		}
	}
	return nil
}

func (s *schema) checkArray(path string, arr []any) error {
	if s.MinItems != nil && len(arr) < *s.MinItems {
		return fmt.Errorf("toolkit: %s 元素数 %d 小于 minItems %d", path, len(arr), *s.MinItems)
	}
	if s.MaxItems != nil && len(arr) > *s.MaxItems {
		return fmt.Errorf("toolkit: %s 元素数 %d 大于 maxItems %d", path, len(arr), *s.MaxItems)
	}
	if s.UniqueItems {
		seen := map[string]bool{}
		for i, item := range arr {
			key := fmt.Sprintf("%v", item)
			if seen[key] {
				return fmt.Errorf("toolkit: %s 第 %d 个元素与前面重复（uniqueItems）", path, i)
			}
			seen[key] = true
		}
	}
	if s.Items == nil {
		return nil
	}
	for i, item := range arr {
		if err := s.Items.check(fmt.Sprintf("%s[%d]", path, i), item); err != nil {
			return err
		}
	}
	return nil
}

func (s *schema) checkType(path string, v any) error {
	if len(s.DeclaredType) > 1 {
		for _, t := range s.DeclaredType {
			cp := *s
			cp.DeclaredType = nil
			cp.Type = t
			if cp.matches(v) {
				return nil
			}
		}
		return fmt.Errorf("toolkit: %s 的类型 %T 不在允许类型 %v 里", path, v, s.DeclaredType)
	}
	if s.Type == "" {
		return nil
	}
	if !s.matches(v) {
		return fmt.Errorf("toolkit: %s 的类型应为 %s，实际是 %T", path, s.Type, v)
	}
	return nil
}

func (s *schema) matches(v any) bool {
	switch s.Type {
	case "":
		return true
	case "object":
		_, ok := v.(map[string]any)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	case "string":
		_, ok := v.(string)
		return ok
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "null":
		return v == nil
	case "integer", "number":
		switch n := v.(type) {
		case json.Number:
			f, err := n.Float64()
			if err != nil {
				return false
			}
			if s.Type == "integer" && f != math.Trunc(f) {
				return false
			}
			if s.Minimum != nil && f < *s.Minimum {
				return false
			}
			if s.Maximum != nil && f > *s.Maximum {
				return false
			}
			return true
		case float64:
			if s.Type == "integer" && n != math.Trunc(n) {
				return false
			}
			if s.Minimum != nil && n < *s.Minimum {
				return false
			}
			if s.Maximum != nil && n > *s.Maximum {
				return false
			}
			return true
		default:
			return false
		}
	}
	return false
}

func enumHas(enum []any, v any) bool {
	for _, e := range enum {
		if fmt.Sprintf("%v", e) == fmt.Sprintf("%v", v) {
			return true
		}
	}
	return false
}

func joinNode(path, name string) string {
	if path == "" || path == "$" {
		return "$." + name
	}
	return path + "." + name
}

func displayNode(path string) string {
	if path == "" {
		return "$"
	}
	return path
}

func anySlice(v any) []any {
	if s, ok := v.([]any); ok {
		return s
	}
	return nil
}

func toStringList(xs []any) ([]string, error) {
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		s, ok := x.(string)
		if !ok {
			return nil, fmt.Errorf("期望字符串，收到 %T", x)
		}
		out = append(out, s)
	}
	return out, nil
}

func number(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}
