package host

// 本文件是「agent 对象与结算」（05 §6.3 的 AgentAPI、06 §6.5 的装配期注册）：
//
//   - 入口工厂收到的 agent 对象、以及与它同一份形状的 agent:host 模块绑定；
//   - 每次调用的入参（payload + ctx），ctx 成员由宿主统一注入（能力位不再决定有没有）；
//   - 返回值结算：工具结果、钩子动作、前端动作、路由响应；
//   - 钩子处理器：把 HookChain 的一次调用排进本实例的 Runtime 队列。
//
// 边界：本文件只在 owner goroutine 上被调用（Runtime 的值不跨 goroutine），
// 跨出去的东西一律是 JSON 字节或宿主长调用（p.hostAsync）。

import (
	"context"
	"fmt"

	"github.com/MoeForge/moejs"
	"github.com/MoeForge/moejs/engine"
)

// runFactory 执行入口工厂并把贡献结算进 staging（06 §6.5）。
//
// 入口的默认导出必须是函数（05 §6.3）；返回 Promise 时等它结算，
// 不结算按「Promise 永不结算」报错，而不是让装配挂住或者静默成功。
func (p *Instance) runFactory(st *Staging) error {
	if p.rt == nil || p.loop == nil {
		return fmt.Errorf("host: 插件 %s 尚未装配运行时", p.id)
	}
	// 入口工厂里的 agent API 从 p.staging 取登记目标。init 路径此前在 runInit
	// 里赋值；rebuild 路径也必须在这里接上，否则工厂会看到 nil staging 并误报
	// 「agent API 已冻结」，插件在第一次超时后的重建就永久失败。
	p.staging = st
	// 让 agent:host 模块的绑定在工厂运行期间能找到本实例（模块体在 loadEntry 时已求值，
	// 但它的导出函数要等到这时才被调用）。
	if cs, ok := stateFor(p.rt); ok {
		cs.inst = p
	}
	api := buildAgentAPI(p.rt, func() *Instance { return p })

	var (
		settled    bool
		factoryErr error
	)
	factoryCall := p.cur
	if factoryCall == nil {
		// 运行时重建（rebuild）没有外层 call：造一个带预算的 init call，
		// 保证装配期 ctx.fetch 的 hostAsync 不会碰到 nil budget。
		factoryCall = newCall(callInit, 0, "entry", nil, p.baseContext(context.Background()), context.Background())
	}
	err := p.loop.Run(func(rt *mojsRuntime) error {
		factory, ok := rt.Export("default")
		if !ok || !engine.IsCallable(factory) {
			return fmt.Errorf("host: 插件 %s 的入口没有 `export default function (agent) {}`（05 §6.3）", p.id)
		}
		res, cerr := rt.CallFunction(factory, moejs.Undefined(), api, p.assemblyContextObject(rt, factoryCall))
		if cerr != nil {
			return cerr
		}
		r := rt.Realm()
		if !res.IsObject() {
			// 工厂没有返回 Promise（05 §6.3 允许 void）：直接算结算完成。
			settled = true
			return nil
		}
		then, terr := r.GetMethod(res, r.KeyFromGoString("then"))
		if terr != nil {
			return terr
		}
		if !engine.IsCallable(then) {
			settled = true
			return nil
		}
		onOk := rt.Function("__ada_factory_ok", 1, func(*moejs.Realm, moejs.Value, []moejs.Value) (moejs.Value, error) {
			settled = true
			return moejs.Undefined(), nil
		})
		onErr := rt.Function("__ada_factory_err", 1, func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
			settled = true
			factoryErr = p.valueError(moejs.Arg(args, 0), factoryCall)
			return moejs.Undefined(), nil
		})
		_, cerr = rt.CallFunction(then, res, onOk, onErr)
		return cerr
	})
	if err != nil {
		return fmt.Errorf("host: 插件 %s 的入口工厂执行失败: %w", p.id, p.exportError(err))
	}
	if factoryErr != nil {
		return fmt.Errorf("host: 插件 %s 的入口工厂被 reject: %w", p.id, factoryErr)
	}
	if !settled {
		return fmt.Errorf("host: 插件 %s 的入口工厂返回的 Promise 没有结算（%v，06 §6.4）", p.id, ErrUnsettled)
	}
	return nil
}

// 拆分说明（纯搬移）：装配期注册 API 与钩子处理器见 register.go，每次调用的
// ctx 组装见 context.go，ctx 子对象（signal/ui/secret/fetch）见 objects.go，
// 脚本值与 Go 结构之间的解码见 decode.go。
