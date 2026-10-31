# AGENTS.md — 面向 AI 代理的仓库工作约定

本文件给在这个仓库里工作的 AI 代理（以及新加入的人类协作者）速查：
当前哪些能力是**有意简化/暂缓**的，动代码前先读这一节，不要把暂缓项当成 bug
修回来，也不要在相关路径上重新加回限制。

## 暂缓：安全与审批（2026-10-31 决策）

以下能力**暂时不做、也不维护**；恢复它们之前，先与本文件和负责人确认。

### 1. 出网 host 黑白名单（allowlist）已移除

- 宿主 HTTP 桥（`internal/plugins/host/httpbridge.go`）当前**不限制目标主机**：
  只要有 `net.fetch` 能力，任意 http(s) 域名都可出网。
- 曾经的 `--allow-host` 参数、`AllowHosts`/`AllowAnyHost` 桥配置、
  供应商端点动态放行（`allowProviderHost`）已全部删除；CLI 参数不要再加回来。
- 保留的边界：协议必须 http(s)、重定向 ≤5 跳、响应 ≤16MB、超时与取消贯穿。
- **后续计划**：黑白名单以**插件**形式实现（策略插件在 `tool.beforeCall` 之外
  新增网络策略点，或独立的网络策略插件读取黑白名单配置），不在宿主桥里写死。
- UI/文档里不要再出现「不在 allowlist 内」类文案；`wttr.in` 等外部 API
  现在可以直接访问。

### 2. 审批策略（policy-default）维持现状，不演进

- `policy-default` 仍在装配里：`access=write/exec/network` 的工具调用继续走
  审批确认；这是现有插件的依赖，**没有移除**。
- 但审批相关的演进（更多模式、策略 UI、白名单免审批等）暂缓，不做新功能。

### 3. 插件沙箱的其余边界保持不变

- 能力位信任矩阵（third-party/verified/builtin）、fs 工作区根、exec 仅
  builtin/verified、响应大小与超时上限**仍然生效**；暂缓的只有 host 黑白名单
  与审批演进两块。

## 其它速查

- 插件安装：全局 `~/.adago/plugins/<id>/`（`plugin.admin` 能力桥，仅 builtin）。
- 插件启停：全局 `~/.adago/plugins.json` + 工作区 `.adag/plugins.json` 覆盖。
- 插件平台 API 规范：`plugins/official/plugin-helper/SPEC.md`；
  操作流程技能：`plugins/official/plugin-helper/skills/create-plugin/SKILL.md`。
- 验证门禁：`go test ./...`、`go vet ./...`、staticcheck、`adag verify`（12 项）。
