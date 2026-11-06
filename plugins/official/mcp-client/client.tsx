// 浏览器 bundle 入口：MCP 服务端管理页使用组件化实现。
import MCPPage from "./page.tsx";

export const islands = {
  "page:servers": MCPPage,
};

export default MCPPage;
