// 浏览器 bundle 入口：page.tsx 与组件化原型共用一份 bundle。
import Page from "./page.tsx";
import ProvidersNext from "./providers-next.tsx";

export const islands = {
  "page:providers": Page,
  "page:providers-next": ProvidersNext,
};

export default Page;
