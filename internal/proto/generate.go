// 生成物入口：spec/proto.json → internal/proto/methods_gen.go。
//
// CI 用 `go generate ./... && git diff --exit-code` 保证生成物与唯一手写源同步（开发计划 §7.1）。
package proto

//go:generate go run github.com/adag/ada-go/tools/genproto
