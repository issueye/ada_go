/* adago-design 画布 · 图标精灵桥（ES module）
 *
 * icons.js 是 Go 侧 go:generate 产出的经典脚本（IIFE），必须先于工具条解析注入
 * <symbol>，因此不改成模块；这里只做一层惰性转发，供模块直接 import。 */
"use strict";

export const icon = (...args) => window.icon(...args);
