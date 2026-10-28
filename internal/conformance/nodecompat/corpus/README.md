# Node 兼容语料（设计 12 §6.14.2）

同一份 JS 语料在 **moejs Runtime**（`corpus_test.go` 的 `TestNodeCompat`）与**真 Node 20 LTS**
（`run-node20.mjs`）各跑一遍，比较返回值、错误 `code`、事件顺序与副作用（§6.14.1）。
差异要么修复，要么进 `../deviations.md` 的偏差表；新增差异先改文档再改实现。

## 目录 = §6.14.2 的九类分层

| 目录 | 层 | 现状 |
|---|---|---|
| `01-modules/` | 模块解析 | 有断言；「未安装模块链接期失败」在 `matrix_test.go`（Node 20 下这些模块都存在，语料表达不了） |
| `02-lang-async/` | 语言与异步 | 有断言 |
| `03-timers/` | 定时器 | 有断言 + 上游缺口 skip |
| `04-binary/` | 二进制 | 有断言 |
| `05-fs/` | fs | 有断言 + 宿主夹逼 skip |
| `06-process-os/` | process + os | 宿主层未落地 → skip-host |
| `07-child-process/` | child_process | ada-go 自建（E-5）→ skip-host |
| `08-crypto-zlib/` | crypto + zlib | ada-go 自建 → skip-host（模块不存在这条有真断言） |
| `09-errors/` | 错误 | 有断言（ENOENT/EISDIR/EACCES/栈映射）+ 宿主码 skip |

## 一个用例 = 两个文件

```
05-fs/fs-01-enoent.mjs            # 语料本体（Node 与 moejs 共用，不含 ada-go 专有分支）
05-fs/fs-01-enoent.expect.json    # moejs v0.1.0-alpha.11 的真实基线（含判定与归属）
```

`*.inc.mjs` 是共享夹具/片段，不需要 `.expect.json`。

### 语料本体

```js
export const meta = { id: "fs-01-enoent", layer: "fs", title: "ENOENT 的错误形状" };

export default async function run(ctx) {
  return {
    returns: { /* 返回值组：表达式结果、typeof、比较布尔 */ },
    codes:   { /* 错误 code 组：每一次失败取 e.code（没有则 e.name，成功则 null） */ },
    order:   [ /* 事件顺序组：按实际 push 顺序的字符串数组 */ ],
    effects: { /* 副作用组：文件列表、计数器、是否真的跑过、宿主侧可见的观察 */ },
  };
}
```

硬约束（否则双跑不可比）：

- 只允许用 `ctx`、`node:*` / 裸内置模块与 ECMAScript 内建；**不许**按 `ctx.host` 分支出两套结果。
- 结果必须可 `JSON.stringify`：不许放函数、Symbol、循环引用（`structuredClone` 类用例除外，它自报布尔）。
- 不许进返回值的东西：时间戳、耗时、随机数、宿主绝对路径、临时目录名。要断言「有时间」就写 `typeof`。
- `readdir` 一类必须排序后再比较。
- 平台做不到的事（例如 Windows 无符号链接权限）用 `return { skip: "原因" }` 上报，
  测试端转成 `t.Skip`，不许把「没跑成」写成「通过」。

`ctx` 由两边的运行器构造，形状一致：

| 字段 | moejs（`corpus_test.go`） | Node 20（`run-node20.mjs`） |
|---|---|---|
| `workspace` | 虚拟根 `"/workspace"`（`node.Options.FS.Mount`） | 真实临时目录 |
| `readOnly` | `FS.Write` 的取反 | 同样语义（Node 里只读靠夹具 chmod，仅作提示） |
| `host` | `"moejs"` | `"node20"`，只供日志，不参与比较 |

夹具由运行器预先铺好（两边同构）：`hello.txt`（内容 `hello moejs`）、`dir/inner.txt`、
`.env`、`out.link -> ../outside/secret.txt`、`inner.link -> hello.txt`；
声明 `runtime.withMany` 时再加 `many/`（10500 个条目）与 `big.bin`（20MB）。

### `.expect.json` 的 schema

```json
{
  "id": "fs-01-enoent",
  "layer": "fs",
  "runtime": {
    "fs": "ro", "buffer": true, "web": false,
    "memLimitBytes": 0, "disableDynamicCode": false,
    "bareRuntime": false, "interruptAfterMs": 0, "stopAfterMs": 0,
    "withMany": false
  },
  "decision": "assert",
  "reason": "…（decision 不是 assert 时必填，写明 E-x 或「等待 M1-C 宿主安装层」）",
  "result": { "returns": {}, "codes": {}, "order": [], "effects": {} },
  "observe": { "rejections": 0, "reported": true, "runErrContains": "", "loadErrContains": "" },
  "nodeParity": "same"
}
```

- `result` 是**子集断言**：只比这里出现的键，值必须相等（数组按下标比）。
  基线由 `ADAG_NODECOMPAT_RECORD=1 go test ./internal/conformance/nodecompat -run TestNodeCompat`
  从真实运行生成，改完语料要复核 diff 再提交（同 `testdata/golden` 的规矩）。
- `decision` 三取一：
  - `assert` —— 真实断言当前行为（已符合的、以及「模块/成员不存在」这类负向事实）；
  - `skip-upstream` —— 上游缺口，`reason` 必须指到 `E-1…E-8`，并在偏差表里有对应行；
  - `skip-host` —— ada-go 宿主安装层（M1-C）才有，`reason` 必须写「等待 M1-C 宿主安装层」。
- `observe` 断言 Go 侧观察：`reported`（JS 是否上报成功）、`runErrContains`（中断/内存这类
  「脚本不可 catch」的终止只能在这里看）、`loadErrContains`（模块求值期失败）、
  `rejections`（未处理 rejection 次数）。
- `nodeParity`：`same` 或 `deviation: <一句话>`（后者必须在 `../deviations.md` 里有行）。

## 在 Node 20 下对跑

```bash
node internal/conformance/nodecompat/corpus/run-node20.mjs            # 全部
node internal/conformance/nodecompat/corpus/run-node20.mjs 05-fs      # 单层
node internal/conformance/nodecompat/corpus/run-node20.mjs --json     # 逐用例 JSON（拿去和基线 diff）
```

runner 自己铺同样的夹具，把每个用例的四组结果打印出来；`--json` 模式逐行输出
`{"id":…,"ok":…,"result":…,"expectDiff":[…]}`，方便和 `.expect.json` 直接比。
Node 侧不要求全绿：它的作用是把「moejs 与 Node 20 的真实差异」摆出来，逐条归档到偏差表。
