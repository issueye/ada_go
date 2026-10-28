// tools/moejspike 的宿主夹具：把探针跑在 moejs v0.1.0-alpha.11 的真实 API 上。
//
// 设计文档 6.12.1 用的是 quickjs 形状的接口名（SetModule/stdlib.Config/rt.Go/AsyncWork），
// alpha.11 的真实表面是另一套：模块表由宿主的 Resolver/Importer 决定，安装由
// node.InstallLoop + webapi.Install 完成，异步由 eventloop.Loop 的 NewPromise/Hold/RunOnLoop 完成。
// 本夹具只暴露真实存在的能力，因此每条探针结论都可归到「moejs 提供」或「宿主必须自己写」。
package main

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/MoeForge/moejs"
	"github.com/MoeForge/moejs/eventloop"
	"github.com/MoeForge/moejs/node"
	"github.com/MoeForge/moejs/webapi"
)

// cfg 是一条探针的宿主配置，字段与 alpha.11 的真实选项一一对应。
type cfg struct {
	// entryName 是插件入口模块的虚拟名字（设计口径 plugin://<id>/index.js）。
	entryName string
	// virtual 是宿主的模块表：specifier -> ESM 源码（编译一次并复用，与真实宿主一致）。
	virtual map[string]string
	// useNode 用上游推荐的组合包住宿主解析器（node.Resolver / node.Importer）。
	useNode bool
	// install 非空时调用 node.InstallLoop；web 非空时调用 webapi.Install。
	install *node.Options
	web     *webapi.Options
	// memLimit / disableDynamicCode / mutableIntrinsics / locale 对应 moejs.Options。
	memLimit            int64
	disableDynamicCode  bool
	mutableIntrinsics   bool
	locale              string
	timeZone            *time.Location
	unrefHostImporter   bool // 只用 node.Importer，不给宿主 virtual 模块（测纯上游路径）
	meta                func(*moejs.Realm, *moejs.Module, *moejs.Object) error
	afterInstall        func(rt *moejs.Runtime) error
	beforeLoad          func(h *hostContext)
	globals             func(h *hostContext)
	noOnError           bool
	bareRuntime         bool          // true：不建 eventloop，直接在裸 Runtime 上 Load
	startLoopWithCancel time.Duration // >0：运行到该时长后 Loop.Interrupt
	loopStopAfter       time.Duration // >0：运行到该时长后 Loop.Stop
}

// lastLoop 留给「关闭后定时器是否还在」这类跨 Run 的探针复用。
var lastLoop *eventloop.Loop

// lastRuntime 是最近一个探针的 Runtime：Loop 结束后宿主可以重新用它求值（moejs 允许）。
var lastRuntime *moejs.Runtime

// afterRunEval 在探针的 Loop 结束之后，用同一个 Runtime 求值一段脚本。
// 用来观察「上一轮留下的状态」：未到期定时器有没有补跑、未结算的 Promise 有没有被拒绝。
func afterRunEval(src string) string {
	if lastRuntime == nil {
		return "无 Runtime"
	}
	s, err := moejs.CompileScript("resume", src)
	if err != nil {
		return "编译失败: " + err.Error()
	}
	if lastLoop != nil {
		_ = lastLoop.Run(func(rt *moejs.Runtime) error { return nil })
	}
	v, err := lastRuntime.RunScript(s)
	if err != nil {
		return errText(err)
	}
	out, err := lastRuntime.ToGo(v)
	if err != nil {
		return "ToGo: " + err.Error()
	}
	return fmt.Sprint(out)
}

// hostContext 给探针安装额外的宿主函数用。
type hostContext struct {
	rt        *moejs.Runtime
	loop      *eventloop.Loop
	out       *outcome
	set       func(name string, fn moejs.NativeFunc)
	closeNode func() // node.API.Close：探针用它实测 Close 之后的行为
}

// outcome 是一条探针的全部可观察结果。
type outcome struct {
	report     string
	reported   bool
	compileErr error
	linkErr    error
	loadErr    error
	runErr     error
	setupErr   error
	cbErrs     []string
	rejections []string
	ranMS      int64
}

// runJS 在 moejs 里编译-链接-加载-运行一个入口模块，返回全部观察结果。
func runJS(entry string, c cfg) *outcome {
	if c.bareRuntime {
		return bareRun(entry, c)
	}
	o := &outcome{}
	// 宿主模块表：真实 ada-go 宿主的做法（bundle 内联 + 已安装的 node:*）。
	cache := map[string]*moejs.Module{}
	hostResolve := func(_ moejs.Referrer, spec string) (*moejs.Module, error) {
		if m, ok := cache[spec]; ok {
			return m, nil
		}
		src, ok := c.virtual[spec]
		if !ok || c.unrefHostImporter {
			if strings.HasPrefix(spec, "node:") {
				return nil, fmt.Errorf("No such built-in module: %s", spec)
			}
			return nil, fmt.Errorf("Cannot find module '%s'", spec)
		}
		m, err := moejs.Compile(spec, src)
		if err != nil {
			return nil, err
		}
		cache[spec] = m
		return m, nil
	}
	resolve := moejs.Resolver(hostResolve)
	if c.useNode {
		resolve = node.Resolver(resolve)
	}
	imp := &moejs.Importer{Resolve: resolve, Meta: c.meta}
	if c.useNode {
		imp = node.Importer(imp)
	}
	name := c.entryName
	if name == "" {
		name = "plugin://spike/index.js"
	}
	rt := moejs.NewRuntime(moejs.Options{
		Importer:           imp,
		MemoryLimit:        c.memLimit,
		DisableDynamicCode: c.disableDynamicCode,
		MutableIntrinsics:  c.mutableIntrinsics,
		Locale:             c.locale,
		TimeZone:           c.timeZone,
	})
	if src := entry; src != "" {
		m, err := moejs.Compile(name, src)
		o.compileErr = err
		if err == nil {
			linked, lerr := moejs.Link(m, resolve)
			o.linkErr = lerr
			if lerr == nil {
				o.runErr = runLoop(rt, linked, o, c)
			}
		}
	} else {
		o.runErr = runLoop(rt, nil, o, c)
	}
	return o
}

// runLoop 建 Loop、装 stdlib、在 Loop 上加载入口模块并跑到结束。
func runLoop(rt *moejs.Runtime, linked *moejs.Module, o *outcome, c cfg) error {
	opts := eventloop.Options{}
	if !c.noOnError {
		opts.OnError = func(err error) { o.cbErrs = append(o.cbErrs, errText(err)) }
	}
	loop, err := eventloop.New(rt, opts)
	if err != nil {
		return fmt.Errorf("eventloop.New: %w", err)
	}
	rt.SetPromiseRejectionTracker(func(p moejs.Value, op moejs.PromiseRejectionOperation) {
		o.rejections = append(o.rejections, fmt.Sprint(op))
	})
	lastLoop = loop
	lastRuntime = rt
	var nodeClose func() // 传给探针，用来实测 API.Close 之后的行为
	if c.install != nil {
		api, ierr := node.InstallLoop(loop, *c.install)
		if ierr != nil {
			o.setupErr = errors.Join(o.setupErr, fmt.Errorf("node.InstallLoop: %w", ierr))
		} else {
			defer api.Close()
			close := api.Close
			nodeClose = func() { _ = close() }
		}
	}
	if c.web != nil {
		api, werr := webapi.Install(loop, *c.web)
		if werr != nil {
			o.setupErr = errors.Join(o.setupErr, fmt.Errorf("webapi.Install: %w", werr))
		} else {
			defer api.Close()
		}
	}
	if c.afterInstall != nil {
		if err := c.afterInstall(rt); err != nil {
			o.setupErr = errors.Join(o.setupErr, err)
		}
	}
	h := &hostContext{rt: rt, loop: loop, out: o, closeNode: nodeClose}
	h.set = func(name string, fn moejs.NativeFunc) {
		if err := rt.SetGlobal(name, fn); err != nil {
			o.setupErr = errors.Join(o.setupErr, fmt.Errorf("SetGlobal %s: %w", name, err))
		}
	}
	start := time.Now()
	runErr := loop.Run(func(rt *moejs.Runtime) error {
		h.set("__report", moejs.NativeFunc(func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
			o.reported = true
			o.report = moejs.Arg(args, 0).String()
			return moejs.Undefined(), nil
		}))
		if c.beforeLoad != nil {
			c.beforeLoad(h)
		}
		if c.globals != nil {
			c.globals(h)
		}
		if c.startLoopWithCancel > 0 {
			time.AfterFunc(c.startLoopWithCancel, func() { loop.Interrupt("spike deadline") })
		}
		if c.loopStopAfter > 0 {
			time.AfterFunc(c.loopStopAfter, func() { loop.Stop() })
		}
		if linked == nil {
			return nil
		}
		if err := rt.Load(linked); err != nil && !errors.Is(err, moejs.ErrModulePending) {
			o.loadErr = err
		}
		return nil
	})
	o.ranMS = time.Since(start).Milliseconds()
	return runErr
}

// errText 打印 Go 错误的动态类型，便于区分 *moejs.ResolveError / *moejs.InterruptedError 等。
func errText(err error) string {
	if err == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%T: %s", err, strings.ReplaceAll(err.Error(), "\n", " | "))
}

// actual 汇总一条探针的实际观察。
func (o *outcome) actual() string {
	var parts []string
	if o.setupErr != nil {
		parts = append(parts, "setupErr="+errText(o.setupErr))
	}
	if o.compileErr != nil {
		parts = append(parts, "compileErr="+errText(o.compileErr))
	}
	if o.linkErr != nil {
		parts = append(parts, "linkErr="+errText(o.linkErr))
	}
	if o.loadErr != nil {
		parts = append(parts, "loadErr="+errText(o.loadErr))
	}
	if o.runErr != nil {
		parts = append(parts, "runErr="+errText(o.runErr))
	}
	for _, e := range o.cbErrs {
		parts = append(parts, "callbackErr="+e)
	}
	if len(o.rejections) > 0 {
		parts = append(parts, "rejections="+strings.Join(o.rejections, ","))
	}
	if o.reported {
		parts = append(parts, "JS="+o.report)
	}
	if len(parts) == 0 {
		parts = append(parts, "JS=<未调用 __report>")
	}
	return strings.Join(parts, "\n         ")
}

// js 返回 __report 的 JSON；没调用时返回空串。
func (o *outcome) js() string { return o.report }

// failed 报告探针是否以 Go 侧错误收场（链接/加载/运行）。
func (o *outcome) failed() bool {
	return o.linkErr != nil || o.loadErr != nil || o.runErr != nil
}

// plainClient 是探针用的默认 http.Client（不访问网络时保持 nil）。
func plainClient() *http.Client { return &http.Client{Timeout: 5 * time.Second} }
