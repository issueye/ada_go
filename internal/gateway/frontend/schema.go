package frontend

// 本文件是插件 block 数据 JSON Schema 的轻量实现（2020-12 的受控子集）。
//
// 只支持 block 作者实际会用的关键字：type / properties / required /
// additionalProperties / enum / const / pattern / minLength / maxLength /
// minimum / maximum / items / minItems / maxItems / allOf / anyOf / oneOf。
// 未知关键字在编译期报错，避免插件作者以为某个约束生效但宿主静默忽略。
//
// 边界：这是「数据形状」校验，不是完整 JSON Schema 实现；需要更强表达式时，
// 应由插件把复杂逻辑放进 TSX，而不是把 schema 当编程语言。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"unicode/utf8"
)

// DataSchema 是编译后的 block 数据 Schema。
type DataSchema struct {
	types       []string
	properties  map[string]*DataSchema
	required    []string
	additional  *bool
	extraSchema *DataSchema
	enum        []any
	constValue  any
	hasConst    bool
	pattern     *regexp.Regexp
	minLength   *int
	maxLength   *int
	minimum     *float64
	maximum     *float64
	items       *DataSchema
	minItems    *int
	maxItems    *int
	allOf       []*DataSchema
	anyOf       []*DataSchema
	oneOf       []*DataSchema
}

var schemaKnownKeys = map[string]bool{
	"$schema": true, "$id": true, "title": true, "description": true,
	"default": true, "examples": true, "$comment": true, "deprecated": true,
	"type": true, "properties": true, "required": true, "additionalProperties": true,
	"enum": true, "const": true, "pattern": true, "minLength": true, "maxLength": true,
	"minimum": true, "maximum": true, "items": true, "minItems": true, "maxItems": true,
	"allOf": true, "anyOf": true, "oneOf": true,
}

// CompileDataSchema 解析并校验一份 block 数据 Schema。
func CompileDataSchema(raw []byte) (*DataSchema, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("block schema: 不是合法的 JSON 对象: %w", err)
	}
	return compileSchema(doc, "$")
}

func compileSchema(doc map[string]any, path string) (*DataSchema, error) {
	for key := range doc {
		if !schemaKnownKeys[key] {
			return nil, fmt.Errorf("block schema: %s 含不支持的字段 %q", path, key)
		}
	}
	s := &DataSchema{}
	if raw, ok := doc["type"]; ok {
		types, err := schemaTypes(raw)
		if err != nil {
			return nil, fmt.Errorf("block schema: %s.type: %w", path, err)
		}
		s.types = types
	}
	if raw, ok := doc["properties"]; ok {
		props, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("block schema: %s.properties 必须是对象", path)
		}
		s.properties = make(map[string]*DataSchema, len(props))
		for name, val := range props {
			child, err := compileSchemaChild(val, path+".properties."+name)
			if err != nil {
				return nil, err
			}
			s.properties[name] = child
		}
	}
	if raw, ok := doc["required"]; ok {
		arr, ok := raw.([]any)
		if !ok {
			return nil, fmt.Errorf("block schema: %s.required 必须是字符串数组", path)
		}
		for _, v := range arr {
			str, ok := v.(string)
			if !ok || str == "" {
				return nil, fmt.Errorf("block schema: %s.required 必须是字符串数组", path)
			}
			s.required = append(s.required, str)
		}
	}
	if raw, ok := doc["additionalProperties"]; ok {
		switch v := raw.(type) {
		case bool:
			s.additional = &v
		case map[string]any:
			child, err := compileSchema(v, path+".additionalProperties")
			if err != nil {
				return nil, err
			}
			s.extraSchema = child
		default:
			return nil, fmt.Errorf("block schema: %s.additionalProperties 只能是布尔或对象", path)
		}
	}
	if raw, ok := doc["enum"]; ok {
		arr, ok := raw.([]any)
		if !ok {
			return nil, fmt.Errorf("block schema: %s.enum 必须是数组", path)
		}
		s.enum = arr
	}
	if raw, ok := doc["const"]; ok {
		s.constValue = raw
		s.hasConst = true
	}
	if raw, ok := doc["pattern"]; ok {
		str, ok := raw.(string)
		if !ok {
			return nil, fmt.Errorf("block schema: %s.pattern 必须是字符串", path)
		}
		re, err := regexp.Compile(str)
		if err != nil {
			return nil, fmt.Errorf("block schema: %s.pattern 非法: %w", path, err)
		}
		s.pattern = re
	}
	if err := intKeyword(doc, "minLength", path, &s.minLength); err != nil {
		return nil, err
	}
	if err := intKeyword(doc, "maxLength", path, &s.maxLength); err != nil {
		return nil, err
	}
	if err := numberKeyword(doc, "minimum", path, &s.minimum); err != nil {
		return nil, err
	}
	if err := numberKeyword(doc, "maximum", path, &s.maximum); err != nil {
		return nil, err
	}
	if raw, ok := doc["items"]; ok {
		child, err := compileSchemaChild(raw, path+".items")
		if err != nil {
			return nil, err
		}
		s.items = child
	}
	if err := intKeyword(doc, "minItems", path, &s.minItems); err != nil {
		return nil, err
	}
	if err := intKeyword(doc, "maxItems", path, &s.maxItems); err != nil {
		return nil, err
	}
	for _, key := range []string{"allOf", "anyOf", "oneOf"} {
		raw, ok := doc[key]
		if !ok {
			continue
		}
		arr, ok := raw.([]any)
		if !ok {
			return nil, fmt.Errorf("block schema: %s.%s 必须是数组", path, key)
		}
		for i, v := range arr {
			child, err := compileSchemaChild(v, fmt.Sprintf("%s.%s[%d]", path, key, i))
			if err != nil {
				return nil, err
			}
			switch key {
			case "allOf":
				s.allOf = append(s.allOf, child)
			case "anyOf":
				s.anyOf = append(s.anyOf, child)
			case "oneOf":
				s.oneOf = append(s.oneOf, child)
			}
		}
	}
	return s, nil
}

func compileSchemaChild(raw any, path string) (*DataSchema, error) {
	doc, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("block schema: %s 必须是对象", path)
	}
	return compileSchema(doc, path)
}

func schemaTypes(raw any) ([]string, error) {
	valid := map[string]bool{
		"null": true, "boolean": true, "object": true, "array": true,
		"string": true, "number": true, "integer": true,
	}
	switch v := raw.(type) {
	case string:
		if !valid[v] {
			return nil, fmt.Errorf("未知类型 %q", v)
		}
		return []string{v}, nil
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			str, ok := item.(string)
			if !ok || !valid[str] {
				return nil, fmt.Errorf("类型数组含非法值 %v", item)
			}
			out = append(out, str)
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("类型数组不能为空")
		}
		return out, nil
	default:
		return nil, fmt.Errorf("type 必须是字符串或字符串数组")
	}
}

func intKeyword(doc map[string]any, key, path string, out **int) error {
	raw, ok := doc[key]
	if !ok {
		return nil
	}
	num, ok := raw.(json.Number)
	if !ok {
		return fmt.Errorf("block schema: %s.%s 必须是整数", path, key)
	}
	n, err := strconv.Atoi(num.String())
	if err != nil || n < 0 {
		return fmt.Errorf("block schema: %s.%s 必须是非负整数", path, key)
	}
	*out = &n
	return nil
}

func numberKeyword(doc map[string]any, key, path string, out **float64) error {
	raw, ok := doc[key]
	if !ok {
		return nil
	}
	num, ok := raw.(json.Number)
	if !ok {
		return fmt.Errorf("block schema: %s.%s 必须是数字", path, key)
	}
	f, err := num.Float64()
	if err != nil {
		return fmt.Errorf("block schema: %s.%s 必须是数字", path, key)
	}
	*out = &f
	return nil
}

// ValidateData 校验一份已解码的 JSON 数据（json.Number 形态）。
func (s *DataSchema) ValidateData(data any) error {
	return s.validate(data, "$", 0)
}

func (s *DataSchema) validate(data any, path string, depth int) error {
	if depth > 32 {
		return fmt.Errorf("block data: 嵌套超过 32 层")
	}
	if s.types != nil && !matchesAnyType(data, s.types) {
		return fmt.Errorf("block data: %s 类型不符合 %v", path, s.types)
	}
	if s.hasConst && !jsonEqual(data, s.constValue) {
		return fmt.Errorf("block data: %s 不等于 const", path)
	}
	if len(s.enum) > 0 {
		ok := false
		for _, v := range s.enum {
			if jsonEqual(data, v) {
				ok = true
				break
			}
		}
		if !ok {
			return fmt.Errorf("block data: %s 不在 enum 中", path)
		}
	}
	switch v := data.(type) {
	case string:
		if s.minLength != nil && utf8.RuneCountInString(v) < *s.minLength {
			return fmt.Errorf("block data: %s 短于 minLength", path)
		}
		if s.maxLength != nil && utf8.RuneCountInString(v) > *s.maxLength {
			return fmt.Errorf("block data: %s 超过 maxLength", path)
		}
		if s.pattern != nil && !s.pattern.MatchString(v) {
			return fmt.Errorf("block data: %s 不匹配 pattern", path)
		}
	case json.Number:
		f, err := v.Float64()
		if err != nil {
			return fmt.Errorf("block data: %s 不是数字", path)
		}
		if s.minimum != nil && f < *s.minimum {
			return fmt.Errorf("block data: %s 小于 minimum", path)
		}
		if s.maximum != nil && f > *s.maximum {
			return fmt.Errorf("block data: %s 大于 maximum", path)
		}
	case []any:
		if s.minItems != nil && len(v) < *s.minItems {
			return fmt.Errorf("block data: %s 少于 minItems", path)
		}
		if s.maxItems != nil && len(v) > *s.maxItems {
			return fmt.Errorf("block data: %s 超过 maxItems", path)
		}
		if s.items != nil {
			for i, item := range v {
				if err := s.items.validate(item, fmt.Sprintf("%s[%d]", path, i), depth+1); err != nil {
					return err
				}
			}
		}
	case map[string]any:
		for _, key := range s.required {
			if _, ok := v[key]; !ok {
				return fmt.Errorf("block data: %s 缺少必填字段 %q", path, key)
			}
		}
		for key, val := range v {
			if child, ok := s.properties[key]; ok {
				if err := child.validate(val, path+"."+key, depth+1); err != nil {
					return err
				}
				continue
			}
			if s.additional != nil && !*s.additional {
				return fmt.Errorf("block data: %s 含未知字段 %q", path, key)
			}
			if s.extraSchema != nil {
				if err := s.extraSchema.validate(val, path+"."+key, depth+1); err != nil {
					return err
				}
			}
		}
	}
	for _, child := range s.allOf {
		if err := child.validate(data, path, depth+1); err != nil {
			return err
		}
	}
	if len(s.anyOf) > 0 {
		ok := false
		for _, child := range s.anyOf {
			if child.validate(data, path, depth+1) == nil {
				ok = true
				break
			}
		}
		if !ok {
			return fmt.Errorf("block data: %s 不满足 anyOf", path)
		}
	}
	if len(s.oneOf) > 0 {
		matched := 0
		for _, child := range s.oneOf {
			if child.validate(data, path, depth+1) == nil {
				matched++
			}
		}
		if matched != 1 {
			return fmt.Errorf("block data: %s 不满足 oneOf", path)
		}
	}
	return nil
}

func matchesAnyType(data any, types []string) bool {
	for _, t := range types {
		switch t {
		case "null":
			if data == nil {
				return true
			}
		case "boolean":
			if _, ok := data.(bool); ok {
				return true
			}
		case "object":
			if _, ok := data.(map[string]any); ok {
				return true
			}
		case "array":
			if _, ok := data.([]any); ok {
				return true
			}
		case "string":
			if _, ok := data.(string); ok {
				return true
			}
		case "number":
			if _, ok := data.(json.Number); ok {
				return true
			}
		case "integer":
			if n, ok := data.(json.Number); ok {
				if _, err := strconv.ParseInt(n.String(), 10, 64); err == nil {
					return true
				}
			}
		}
	}
	return false
}

func jsonEqual(a, b any) bool {
	an, aok := a.(json.Number)
	bn, bok := b.(json.Number)
	if aok && bok {
		af, aerr := an.Float64()
		bf, berr := bn.Float64()
		return aerr == nil && berr == nil && af == bf
	}
	if aok != bok {
		return false
	}
	return reflect.DeepEqual(a, b)
}
