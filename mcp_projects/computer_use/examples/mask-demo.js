// 在运行中的程序里执行：computer-use script examples\mask-demo.js
// 脚本里的 cu 就是全部动作；返回值是最后一个表达式的值。
const before = cu.cursor();
cu.log("cursor before", before.x, before.y);

// 在指针周围画一个带坐标的框，并写一行状态
cu.mask({
  regions: [{ rect: { x: before.x - 120, y: before.y - 60, w: 240, h: 120 }, label: "脚本标注 · from script", index: 1 }],
  markers: [{ at: before, label: "指针" }],
  hud: { detail: "脚本标注中 · script running" }
});

// 等 300ms 让用户看见，然后清掉
cu.wait(300);
cu.clearMask();

({ cursor: before, windows: cu.windows().length, displays: cu.state().displays.length });
