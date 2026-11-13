/* adago-design 画布 · HTTP 封装与连接状态（ES module） */
"use strict";

import { $, S } from "./state.js";
import { setTip } from "./ui.js";

export async function api(path, opts) {
  let res;
  try {
    res = await fetch(path, opts);
  } catch (e) {
    setOnline(false);
    throw e;
  }
  setOnline(true);
  const data = await res.json().catch(() => ({}));
  if (!res.ok) {
    const err = data && data.error || { message: res.statusText };
    const e = new Error(err.message || res.statusText);
    e.code = err.code; e.hint = err.hint;
    throw e;
  }
  return data;
}

export function setOnline(ok) {
  if (ok === S.online) return;
  S.online = ok;
  const c = $("conn");
  c.classList.toggle("conn--ok", ok);
  c.classList.toggle("conn--bad", !ok);
  setTip("conn", ok ? "已连接" : "连接断开");
}
