// Package sqlite 是 ports.SessionStore / ports.StateStore 的生产实现（M1-B）。
//
// 定位：每个工作区一个 SQLite 数据库（<dataDir>/state.db），WAL 模式、写者唯一（04 §5.7）。
// 本包同时承担三件相邻的事：
//
//   - 工作区 OS 独占锁（<dataDir>/locks/ws-<hash>.lock）：拿不到锁直接返回
//     workspace_locked（含持有者 pid），并且不去打开数据库（04 §5.7）；
//   - schema 与事务迁移（PRAGMA user_version）：只有持锁的 Core 在启动时迁移，
//     失败回滚并拒绝启动；doctor 只报告版本（04 §5.7）；
//   - 单写者协程 + WAL 读连接：所有写操作进有界队列由唯一写协程串行提交，
//     seq 也在该协程内分配（04 §5.7）。
//
// 依赖方向（设计 13 约束、01 §3.3）：只 import 标准库 + internal/domain +
// internal/ports + internal/diag + 已锁定的第三方库（modernc.org/sqlite、gofrs/flock、
// oklog/ulid）。internal/diag 是只依赖标准库的叶子词汇表包，也是「应用错误码 → 诊断码」
// 映射的唯一位置（codes.go 自述），本包用它取诊断码而不是把 ADG-STORE-001 这类数字抄一份
// 进适配器（抄一份就是第二真源）。不 import internal/engine、internal/coreapp、
// internal/gateway、internal/plugins，也不 import 父包 internal/adapters
// （避免任何横向耦合）。无 cgo：驱动固定 modernc.org/sqlite，保持 CGO_ENABLED=0
// 的三平台交叉编译（11 §13.2）。
//
// 「Gateway 不打开 state.db」这条约束在代码里由什么保证（04 §5.7）：
//
//  1. 写入口只有一个：OpenCore 是唯一的写打开路径，而它在打开数据库之前必须先
//     取得 OS 独占锁；锁是每工作区一份的内核对象句柄，第二个进程（第二个 Core）
//     直接拿不到，Gateway 即使误调也用同一个错误面被拒。
//  2. Gateway 侧需要的读路径不在本包里暴露写能力：OpenReadOnly 只创建
//     mode=ro + query_only=ON 的连接，任何写语句在 SQLite 层就是 SQLITE_READONLY；
//     它也不迁移 schema（版本不符直接 storage_corrupt 报错），所以「读的人」在
//     物理上和语义上都无法变成第二写者。
//  3. 会话读写的唯一通道是 Core RPC（coreproto，M2）：本包的类型不导出任何
//     *sql.DB / *sql.Tx，gateway 包即便拿到 Store 也只能走端口方法，而端口方法
//     的写都经过那条唯一写队列。
//
// 失败方向（09 §9.1、INV-4）：任何拿不准的持久化/锁/网络盘判定一律往「拒绝启动、
// 拒绝写」倒，不静默降级、不自动删除 -wal/-shm、不悄悄丢 entry。
package sqlite
