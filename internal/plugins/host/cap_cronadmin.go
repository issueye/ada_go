package host

// ctx.cron —— 定时任务模块的管理面（capability: cron.admin，仅 builtin）。
//
// 只通过 internal/cronstore 读写固定文件 ~/.adago/cron.json（0600 原子写，
// 跨进程文件锁）：脚本不能传路径。任务校验（cron 表达式、动作形状、大小上限）
// 都在 cronstore；真正的调度与执行在独立 cron 二进制（mcp_projects/cron），
// 插件只能改定义与打「立即运行」标记——这是刻意的能力切分：管理面不等于执行面。
//
// 成员（与插件平台 SPEC §4 的 ctx 能力表同步）：
//   - list()                    任务列表（含 nextRunAt 预览）+ 警告；
//   - create(task)              新建（id/时间戳由宿主生成）；
//   - update(id, task)          整体替换定义字段（runs/threadId 等运行态不动）；
//   - remove(id)                删除；
//   - setEnabled(id, enabled)   启停；
//   - trigger(id)               打「立即运行」标记（cron 二进制下个 tick 消费）；
//   - runs(id, limit?)          运行历史（最新在前）。
//
// 审计只记 id、名称与动作类型（固定为 agent）：提示词不进审计日志。

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/MoeForge/moejs"
	"github.com/MoeForge/moejs/engine"

	"github.com/adag/ada-go/internal/cronstore"
	"github.com/adag/ada-go/internal/plugins"
)

// errCronTaskNotFound 是 update/setEnabled/trigger 找不到任务时的判定标记。
var errCronTaskNotFound = errors.New("任务不存在")

// cronWait 是跨进程任务文件锁的最长等待：与 cron 二进制的写窗口（毫秒级）相比
// 足够宽裕，又不会在锁异常时把页面请求吊死太久。
const cronWait = 5 * time.Second

// cronTaskView 是 list() 里单个任务的展示形状：定义字段 + nextRunAt 预览。
type cronTaskView struct {
	cronstore.Task
	NextRunAt string `json:"nextRunAt,omitempty"`
}

// addCronAdminContextMember 给 ToolContext 补 ctx.cron（capability: cron.admin）。
func addCronAdminContextMember(st *capsState, hc Context, put func(string, moejs.Value) error) error {
	if st == nil || st.caps == nil || !st.caps.Has(plugins.CapCronAdmin) {
		return nil
	}
	r := st.rt.Realm()

	// updateGlobal 在文件锁内跑一段读改写；路径解析失败按越界处理。
	updateGlobal := func(rr *engine.Realm, fn func(tasks *[]cronstore.Task) error) (moejs.Value, error) {
		path, err := cronstore.GlobalPath()
		if err != nil {
			return moejs.Undefined(), throwCoded(rr, codeOutOfRange, "ctx.cron: "+err.Error(), nil)
		}
		lock, err := cronstore.LockPath()
		if err != nil {
			return moejs.Undefined(), throwCoded(rr, codeOutOfRange, "ctx.cron: "+err.Error(), nil)
		}
		if err := cronstore.Update(path, lock, cronWait, fn); err != nil {
			return moejs.Undefined(), throwCoded(rr, codeOutOfRange, "ctx.cron: "+err.Error(), nil)
		}
		return moejs.Undefined(), nil
	}

	// decodeArg 把实参摊成 JSON 再解到 out；undefined/null 视作缺失。
	decodeArg := func(rr *engine.Realm, what string, v moejs.Value, out any) error {
		raw, err := st.rt.AppendJSON(nil, v)
		if err != nil || len(raw) == 0 || string(raw) == "null" {
			return fmt.Errorf("ctx.cron.%s: 缺少实参", what)
		}
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("ctx.cron.%s: 实参形状不符: %v", what, err)
		}
		return nil
	}

	obj := r.NewObject()
	set := func(name string, arity int, fn func(rr *engine.Realm, args []moejs.Value) (moejs.Value, error)) {
		_ = obj.SetProp(r, r.KeyFromGoString(name), st.rt.Function("cron."+name, arity,
			func(rr *engine.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
				return fn(rr, args)
			}))
	}

	set("list", 0, func(rr *engine.Realm, args []moejs.Value) (moejs.Value, error) {
		path, err := cronstore.GlobalPath()
		if err != nil {
			return moejs.Undefined(), throwCoded(rr, codeOutOfRange, "ctx.cron.list: "+err.Error(), nil)
		}
		f, warns, err := cronstore.LoadFile(path)
		if err != nil {
			return moejs.Undefined(), throwCoded(rr, codeOutOfRange, "ctx.cron.list: "+err.Error(), nil)
		}
		now := time.Now()
		views := make([]cronTaskView, 0, len(f.Tasks))
		for i := range f.Tasks {
			view := cronTaskView{Task: f.Tasks[i]}
			if f.Tasks[i].Enabled {
				if next, err := cronstore.NextAfter(&f.Tasks[i], now); err == nil && !next.IsZero() {
					view.NextRunAt = next.Format(time.RFC3339)
				}
			}
			views = append(views, view)
		}
		return st.rt.FromGo(map[string]any{
			"path":     baseForLog(path),
			"tasks":    views,
			"warnings": warns,
		})
	})

	set("create", 1, func(rr *engine.Realm, args []moejs.Value) (moejs.Value, error) {
		var task cronstore.Task
		if err := decodeArg(rr, "create", moejs.Arg(args, 0), &task); err != nil {
			return moejs.Undefined(), throwCoded(rr, codeInvalidArgType, err.Error(), nil)
		}
		id, err := cronstore.NewID()
		if err != nil {
			return moejs.Undefined(), throwCoded(rr, codeOutOfRange, "ctx.cron.create: "+err.Error(), nil)
		}
		task.ID = id
		now := time.Now().UTC().Format(time.RFC3339)
		task.CreatedAt, task.UpdatedAt = now, now
		task.Runs, task.ThreadID, task.LastFiredAt, task.TriggerRequested = nil, "", "", false
		if err := cronstore.ValidateTask(&task); err != nil {
			return moejs.Undefined(), throwCoded(rr, codeInvalidArgType, "ctx.cron.create: "+err.Error(), nil)
		}
		if _, err := updateGlobal(rr, func(tasks *[]cronstore.Task) error {
			if len(*tasks) >= cronstore.MaxTasks {
				return fmt.Errorf("任务数超过上限 %d", cronstore.MaxTasks)
			}
			*tasks = append(*tasks, task)
			return nil
		}); err != nil {
			return moejs.Undefined(), err
		}
		st.audit("cron", "create", task.ID, 0, "ok", map[string]any{"name": task.Name, "action": task.Action.Type})
		return st.rt.FromGo(map[string]any{"id": task.ID, "createdAt": task.CreatedAt})
	})

	set("update", 1, func(rr *engine.Realm, args []moejs.Value) (moejs.Value, error) {
		var body struct {
			ID   string         `json:"id"`
			Task cronstore.Task `json:"task"`
		}
		if err := decodeArg(rr, "update", moejs.Arg(args, 0), &body); err != nil {
			return moejs.Undefined(), throwCoded(rr, codeInvalidArgType, err.Error(), nil)
		}
		if strings.TrimSpace(body.ID) == "" {
			return moejs.Undefined(), throwCoded(rr, codeInvalidArgType, "ctx.cron.update: 需要 id", nil)
		}
		found := false
		if _, err := updateGlobal(rr, func(tasks *[]cronstore.Task) error {
			for i := range *tasks {
				if (*tasks)[i].ID != body.ID {
					continue
				}
				prev := (*tasks)[i]
				next := body.Task
				next.ID = prev.ID
				next.CreatedAt = prev.CreatedAt
				next.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
				// 启停与运行态字段不通过 update 修改：enabled 归 setEnabled 专属
				// （编辑停用中的任务不会把它意外重新启用），其余归 cron 二进制。
				next.Enabled = prev.Enabled
				next.Workspace = strings.TrimSpace(next.Workspace)
				next.Runs, next.LastFiredAt = prev.Runs, prev.LastFiredAt
				next.TriggerRequested = prev.TriggerRequested
				// 任务专用线程属于旧工作区：换工作区后线程 id 失效，清掉让下次
				// 运行在新工作区重建（否则会连续失败一次再自愈）。
				next.ThreadID = prev.ThreadID
				if next.Workspace != prev.Workspace {
					next.ThreadID = ""
				}
				if err := cronstore.ValidateTask(&next); err != nil {
					return err
				}
				(*tasks)[i] = next
				found = true
				break
			}
			if !found {
				return fmt.Errorf("任务 %q: %w", body.ID, errCronTaskNotFound)
			}
			return nil
		}); err != nil {
			if errors.Is(err, errCronTaskNotFound) {
				return moejs.Undefined(), throwCoded(rr, codeInvalidArgType, "ctx.cron.update: "+err.Error(), nil)
			}
			return moejs.Undefined(), err
		}
		st.audit("cron", "update", body.ID, 0, "ok", map[string]any{"name": body.Task.Name})
		return st.rt.FromGo(map[string]any{"id": body.ID, "updated": true})
	})

	set("remove", 1, func(rr *engine.Realm, args []moejs.Value) (moejs.Value, error) {
		var body struct {
			ID string `json:"id"`
		}
		if err := decodeArg(rr, "remove", moejs.Arg(args, 0), &body); err != nil {
			return moejs.Undefined(), throwCoded(rr, codeInvalidArgType, err.Error(), nil)
		}
		removed := false
		if _, err := updateGlobal(rr, func(tasks *[]cronstore.Task) error {
			for i := range *tasks {
				if (*tasks)[i].ID == body.ID {
					*tasks = append((*tasks)[:i], (*tasks)[i+1:]...)
					removed = true
					return nil
				}
			}
			return nil
		}); err != nil {
			return moejs.Undefined(), err
		}
		st.audit("cron", "remove", body.ID, 0, "ok", nil)
		return st.rt.FromGo(map[string]any{"id": body.ID, "removed": removed})
	})

	set("setEnabled", 1, func(rr *engine.Realm, args []moejs.Value) (moejs.Value, error) {
		var body struct {
			ID      string `json:"id"`
			Enabled bool   `json:"enabled"`
		}
		if err := decodeArg(rr, "setEnabled", moejs.Arg(args, 0), &body); err != nil {
			return moejs.Undefined(), throwCoded(rr, codeInvalidArgType, err.Error(), nil)
		}
		updated := false
		if _, err := updateGlobal(rr, func(tasks *[]cronstore.Task) error {
			for i := range *tasks {
				if (*tasks)[i].ID == body.ID {
					(*tasks)[i].Enabled = body.Enabled
					(*tasks)[i].UpdatedAt = time.Now().UTC().Format(time.RFC3339)
					updated = true
					return nil
				}
			}
			return nil
		}); err != nil {
			return moejs.Undefined(), err
		}
		st.audit("cron", "setEnabled", body.ID, 0, "ok", map[string]any{"enabled": body.Enabled})
		return st.rt.FromGo(map[string]any{"id": body.ID, "enabled": body.Enabled, "updated": updated})
	})

	set("trigger", 1, func(rr *engine.Realm, args []moejs.Value) (moejs.Value, error) {
		var body struct {
			ID string `json:"id"`
		}
		if err := decodeArg(rr, "trigger", moejs.Arg(args, 0), &body); err != nil {
			return moejs.Undefined(), throwCoded(rr, codeInvalidArgType, err.Error(), nil)
		}
		marked := false
		if _, err := updateGlobal(rr, func(tasks *[]cronstore.Task) error {
			for i := range *tasks {
				if (*tasks)[i].ID == body.ID {
					(*tasks)[i].TriggerRequested = true
					(*tasks)[i].UpdatedAt = time.Now().UTC().Format(time.RFC3339)
					marked = true
					return nil
				}
			}
			return nil
		}); err != nil {
			return moejs.Undefined(), err
		}
		st.audit("cron", "trigger", body.ID, 0, "ok", nil)
		return st.rt.FromGo(map[string]any{"id": body.ID, "triggered": marked})
	})

	set("runs", 1, func(rr *engine.Realm, args []moejs.Value) (moejs.Value, error) {
		var body struct {
			ID    string `json:"id"`
			Limit int    `json:"limit"`
		}
		if err := decodeArg(rr, "runs", moejs.Arg(args, 0), &body); err != nil {
			return moejs.Undefined(), throwCoded(rr, codeInvalidArgType, err.Error(), nil)
		}
		path, err := cronstore.GlobalPath()
		if err != nil {
			return moejs.Undefined(), throwCoded(rr, codeOutOfRange, "ctx.cron.runs: "+err.Error(), nil)
		}
		f, _, err := cronstore.LoadFile(path)
		if err != nil {
			return moejs.Undefined(), throwCoded(rr, codeOutOfRange, "ctx.cron.runs: "+err.Error(), nil)
		}
		runs := []cronstore.Run{}
		for i := range f.Tasks {
			if f.Tasks[i].ID != body.ID {
				continue
			}
			runs = append(runs, f.Tasks[i].Runs...)
			break
		}
		sort.SliceStable(runs, func(a, b int) bool { return runs[a].At > runs[b].At })
		if body.Limit > 0 && len(runs) > body.Limit {
			runs = runs[:body.Limit]
		}
		return st.rt.FromGo(map[string]any{"id": body.ID, "runs": runs})
	})

	return put("cron", engine.ObjectValue(obj))
}
