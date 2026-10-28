# M5 发布清单（v1.0.0-rc.1）

> 对应章节：[09-security-operations.md](09-security-operations.md)（审计与备份）、
> [development-plan.md §4.6](development-plan.md)（M5 退出标准）、
> [performance.md](performance.md)（性能基线）。

## 1. 版本与构建

- 版本号真源：`internal/version` 的 `Version`（默认 `1.0.0-rc.1`）；
  `make release` 注入规则是 **git tag 优先于 rev**：HEAD 恰好落在 tag 上时用
  `git describe --tags --exact-match`（如 `v1.0.0-rc.1`），否则落回短哈希；
  也可显式覆盖：`make release RELEASE_VERSION=v1.0.0-rc.1`。
  `Revision`/`BuildTime` 照旧由 ldflags 注入（01 §4.2、08 §8.2 的版本字段）。
- 构建命令：`make release`（GOWORK=off、CGO_ENABLED=0，与 CI release 矩阵同口径）。
  注意：Makefile 的 recipe 字符串保持 ASCII——mingw32-make 在 Windows 上按系统
  代码页传参，中文 recipe 字符串会让 sh 引号配对错乱（注释不受影响）。
- 本机（Windows）验证：`make release` 产出三平台二进制（linux/darwin/windows 的
  amd64 与 darwin/arm64）与 sha256 清单；交叉编译失败只告警不阻塞，但至少要有
  一个产物，否则报错（不允许「成功地什么都没发布」）。

## 2. 三平台产物清单

`make release` 产出 `bin/release/`：

| 产物 | 说明 |
|---|---|
| `adag-<version>-linux-amd64` | Linux x86_64（musl/glibc 无关：纯静态，CGO=0） |
| `adag-<version>-darwin-amd64` | macOS Intel |
| `adag-<version>-darwin-arm64` | macOS Apple Silicon |
| `adag-<version>-windows-amd64.exe` | Windows x86_64 |
| `sha256sums.txt` | 全部二进制的 SHA-256 清单（升级校验用，随产物一起发布） |

产物自检：`adag version` 应显示注入的版本号与构建信息；`adag doctor --fake` 应全绿。

## 3. 升级步骤

1. **备份**（升级前必做）：
   - `adag backup --to <file>` —— 在线一致性备份（VACUUM INTO，含 state.db 一致副本、
     插件与配置；**不含密钥**，见 09 §10）；
   - `adag export --thread <id> [--out file]` —— 按线程导出 JSONL/Markdown 快照；
   - 检查点备份目录在数据目录 `changes/` 下（每个检查点一个子目录：文件备份 +
     `manifest.json`）；如需整机迁移请连同数据目录一起拷贝。
2. **替换二进制**：用新 `adag` 覆盖旧文件；用 `sha256sums.txt` 校验下载完整性。
3. **启动**：`adag gateway`（Core 由监督器按需拉起）。启动时 SQLite migration
   事务化、幂等；插件 `apiVersion` 不兼容会被禁用并提示，不阻止主程序启动（09 §10）。
4. **验证**：`adag doctor`（SQLite 完整性/WAL/schema、工作区锁、Gateway 端口、
   provider 配置形状）；UI 上确认历史线程可见、`plugin list` 与升级前一致。

## 4. 恢复步骤

1. 停止 gateway（Core 随之退出；工作区锁释放）。
2. 用 `adag backup` 产物覆盖数据目录中的 `state.db`（备份是 VACUUM INTO 的
   一致副本，直接可用）；不要手工拷贝运行中的 `state.db/-wal/-shm`。
3. `adag doctor --fake` 确认完整性后再启动 gateway。
4. 文件级回滚（会话内）：`change.revert`（回滚卡片/文件/全部）恢复检查点备份；
   命令行检查：`adag audit list` 里查 `storage.restore` 记录。

## 5. 审计接线（本里程碑新增）

`audit` 表（04 §5.7）在本里程碑接入的动作：`plugin.changed`（plugin.setEnabled）、
`plugin.reload`（plugin.reload）、`turn.cancel`、`storage.restore`（change.revert）。
只读查看：

```sh
adag audit list --limit 50 [--since 2026-10-01T00:00:00Z]
```

## 6. 已知限制（发布时如实声明）

| 限制 | 说明 | 计划 |
|---|---|---|
| race 仅 CI Linux | `-race` 依赖 cgo；Windows 本机无 MSVC/clang 时降级为普通测试（Makefile M0 注释） | CI 矩阵补 Windows race 腿 |
| 非回环 TLS 未做 | gateway/core 只承诺回环绑定；非回环监听要求 `ADAG_CORE_TOKEN`，但没有 TLS 终结 | v1.1（反代终止 TLS 是受支持部署） |
| 插件市场未做 | 插件安装走本地目录/zip（`adag plugin install`）；无签名/市场分发 | v1.1+ |
| node:fs 原生写成员不进检查点 | `ctx.fs.writeText` 与官方文件工具的写入有检查点；`fs.writeFile` 等 node:fs 兼容成员的写入也会并入检查点，但 `rm/unlink/rename` 等删除/改名动作不进检查点（回滚不覆盖它们） | v1.1 扩展删除类动作 |
| 子智能体未交付 | 按决策保持 v1.5 预研，不进 v1.0 | v1.5 |
