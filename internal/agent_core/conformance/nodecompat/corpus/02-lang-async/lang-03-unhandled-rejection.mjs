// lang-03: 未处理 rejection 交给宿主钩子而不是结束进程（设计 6.11 Promise 与 job 行）。
// Go 侧再看 observe.rejections。
export const meta = { id: "lang-03-unhandled-rejection", layer: "lang-async", title: "未处理 rejection" };

export default async function run() {
  Promise.reject(new Error("没有人接"));
  const caught = [];
  Promise.reject(new Error("有人接")).catch((e) => caught.push(e.message));
  await new Promise((res) => setTimeout(res, 20));
  return {
    returns: { alive: true, caught: caught.join(",") },
    codes: {},
    order: ["rejected-then-still-alive"],
    effects: {},
  };
}
