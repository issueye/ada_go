// 浏览器 bundle 入口：环境变量页使用组件化实现。
import EnvPage from "./env-page.tsx";

export const islands = {
  "page:settings": EnvPage,
};

export default EnvPage;
