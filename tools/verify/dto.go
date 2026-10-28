package verify

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/adag/ada-go/internal/proto"
)

// dtoBindings 把 spec/proto.json 的 definitions 绑到手写 DTO（设计 8.2：
// 「DTO 手写在 dto.go，由契约测试逐字段对照 Schema」）。
//
// 被 $ref 引用的 definition 由请求/响应装配使用；没被引用但在此登记的，
// 靠这张表证明它有消费者。两边都不在的 definition 视为孤儿，verify 失败。
func dtoBindings() map[string]reflect.Type {
	return map[string]reflect.Type{
		"ToolDescriptor":      reflect.TypeOf(proto.ToolDescriptor{}),
		"ToolReceipt":         reflect.TypeOf(proto.ToolReceipt{}),
		"ToolResult":          reflect.TypeOf(proto.ToolResult{}),
		"ToolContentPart":     reflect.TypeOf(proto.ContentPart{}),
		"Interaction":         reflect.TypeOf(proto.Interaction{}),
		"Item":                reflect.TypeOf(proto.Item{}),
		"RunState":            reflect.TypeOf(proto.RunState{}),
		"Usage":               reflect.TypeOf(proto.Usage{}),
		"PluginSummary":       reflect.TypeOf(proto.PluginSummary{}),
		"ModelRef":            reflect.TypeOf(proto.ModelRef{}),
		"ModelInfo":           reflect.TypeOf(proto.ModelInfo{}),
		"WorkspaceInfo":       reflect.TypeOf(proto.WorkspaceInfo{}),
		"ChangeSet":           reflect.TypeOf(proto.ChangeSet{}),
		"RouteRequest":        reflect.TypeOf(proto.RouteRequest{}),
		"SkillSummary":        reflect.TypeOf(proto.SkillSummary{}),
		"ProductCapabilities": reflect.TypeOf(proto.ProductCapabilities{}),
		"ProductPolicy":       reflect.TypeOf(proto.ProductPolicies{}),
		"ProductDeclaration":  reflect.TypeOf(proto.ProductDeclaration{}),
	}
}

// definitionProps 取某个 definition 的顶层属性名。
func definitionProps(defs map[string]json.RawMessage, name string) ([]string, error) {
	raw, ok := defs[name]
	if !ok {
		return nil, fmt.Errorf("definitions 里没有 %s", name)
	}
	var node struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(raw, &node); err != nil {
		return nil, fmt.Errorf("definition %s 解析失败: %w", name, err)
	}
	out := make([]string, 0, len(node.Properties))
	for k := range node.Properties {
		out = append(out, k)
	}
	sort.Strings(out)
	return out, nil
}

// topJSONFields 取一个结构体类型的顶层 json 字段名。
func topJSONFields(t reflect.Type) []string {
	var out []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" {
			continue
		}
		tag, ok := f.Tag.Lookup("json")
		if !ok {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name == "" || name == "-" {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// checkDTOBindings 逐字段对照 Schema 与手写 DTO。
func checkDTOBindings(defs map[string]json.RawMessage) []string {
	var problems []string
	names := make([]string, 0, len(dtoBindings()))
	for name := range dtoBindings() {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		shape, err := definitionProps(defs, name)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		fields := topJSONFields(dtoBindings()[name])
		missing, extra := setDiff(shape, fields)
		if len(missing) > 0 || len(extra) > 0 {
			problems = append(problems, fmt.Sprintf("DTO %s 与 Schema definition 不同步：Go 缺 %v，Go 多 %v", name, missing, extra))
		}
	}
	return problems
}

// eventPayloadBindings 把手写 DTO 绑到事件载荷（8.2 事件表）。
//
// 与 definitions 同一套纪律：载荷字段漂移由 adag verify 拦下，不靠前端「大概是这样」。
func eventPayloadBindings() map[string]reflect.Type {
	return map[string]reflect.Type{
		"evt.ready":                 reflect.TypeOf(proto.ReadyEvent{}),
		"evt.state.snapshot":        reflect.TypeOf(proto.Snapshot{}),
		"evt.state.patch":           reflect.TypeOf(proto.Patch{}),
		"evt.message.delta":         reflect.TypeOf(proto.MessageDelta{}),
		"evt.item.updated":          reflect.TypeOf(proto.ItemUpdated{}),
		"evt.run.status":            reflect.TypeOf(proto.RunStatusEvent{}),
		"evt.interaction.requested": reflect.TypeOf(proto.InteractionRequested{}),
		"evt.interaction.resolved":  reflect.TypeOf(proto.InteractionResolved{}),
		"evt.notice":                reflect.TypeOf(proto.Notice{}),
		"evt.usage":                 reflect.TypeOf(proto.UsageDelta{}),
		"evt.plugin.changed":        reflect.TypeOf(proto.PluginChanged{}),
		"evt.error":                 reflect.TypeOf(proto.ErrorEvent{}),
	}
}

// checkEventDTOs 逐字段对照事件载荷 Schema 与 DTO。
func checkEventDTOs(events []specEvent) []string {
	var problems []string
	bindings := eventPayloadBindings()
	names := make([]string, 0, len(bindings))
	for name := range bindings {
		names = append(names, name)
	}
	sort.Strings(names)
	byName := map[string]specEvent{}
	for _, e := range events {
		byName[e.Name] = e
	}
	for _, name := range names {
		ev, ok := byName[name]
		if !ok {
			problems = append(problems, fmt.Sprintf("协议里没有事件 %s，但 DTO 绑定表引用了它", name))
			continue
		}
		var node struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(ev.Payload, &node); err != nil {
			problems = append(problems, fmt.Sprintf("事件 %s 的 payload 解析失败: %v", name, err))
			continue
		}
		shape := make([]string, 0, len(node.Properties))
		for k := range node.Properties {
			shape = append(shape, k)
		}
		sort.Strings(shape)
		fields := topJSONFields(bindings[name])
		missing, extra := setDiff(shape, fields)
		if len(missing) > 0 || len(extra) > 0 {
			problems = append(problems, fmt.Sprintf("事件 %s 的 DTO 与载荷 Schema 不同步：Go 缺 %v，Go 多 %v", name, missing, extra))
		}
	}
	return problems
}

// unboundDefinitions 返回既没有被 $ref 使用、也没有 DTO 绑定的 definition。
func unboundDefinitions(defs map[string]json.RawMessage, refs map[string]int) []string {
	bindings := dtoBindings()
	var out []string
	for name := range defs {
		if refs["#/definitions/"+name] > 0 {
			continue
		}
		if _, ok := bindings[name]; ok {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
