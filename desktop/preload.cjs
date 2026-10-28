// ada-go 桌面壳 preload：从 a_da 的 electron/preload.ts 迁移（MIT，commit ae2f8ffe…）。
//
// 两条职责：
//  1. contextBridge 暴露 window.electronAPI（网关发现 / 窗口控制 / 配置 / 进程管理）；
//  2. 无边框窗口控制：把 .ada-topbar 变成拖拽区，并在右侧动作区追加
//     最小化/最大化/关闭按钮（ada-go SSR 与 a_da 用同一套 .ada-* DOM，注入逻辑原样）。

const { contextBridge, ipcRenderer } = require('electron')

contextBridge.exposeInMainWorld('electronAPI', {
  getCoreInfo: () => ipcRenderer.invoke('desktop:get-core-info'),
  getDesktopConfig: () => ipcRenderer.invoke('desktop:get-config'),
  setDesktopConfig: (cfg) => ipcRenderer.invoke('desktop:set-config', cfg),
  restartDesktopApp: () => ipcRenderer.invoke('desktop:restart'),
  restartGateway: () => ipcRenderer.invoke('desktop:restart-gateway'),
  getProcessReport: () => ipcRenderer.invoke('desktop:get-process-report'),
  killProcess: (pid) => ipcRenderer.invoke('desktop:kill-process', pid),
  openWebUi: () => ipcRenderer.invoke('desktop:open-web-ui'),
  windowMinimize: () => ipcRenderer.invoke('desktop:window-minimize'),
  windowMaximize: () => ipcRenderer.invoke('desktop:window-maximize'),
  windowClose: () => ipcRenderer.invoke('desktop:window-close'),
  isWindowMaximized: () => ipcRenderer.invoke('desktop:window-is-maximized'),
  onWindowMaximizeChange: (callback) => {
    const handler = (_e, val) => callback(val)
    ipcRenderer.on('window:maximize-change', handler)
    return () => ipcRenderer.removeListener('window:maximize-change', handler)
  },
})

// ── 无边框窗口控制 ──────────────────────────────────────────────────────
const ICON_MINIMIZE =
  '<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" aria-hidden="true"><path d="M5 12h14"/></svg>'
const ICON_MAXIMIZE =
  '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linejoin="round" aria-hidden="true"><rect x="5" y="5" width="14" height="14" rx="1.5"/></svg>'
const ICON_RESTORE =
  '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linejoin="round" aria-hidden="true"><rect x="4" y="8" width="12" height="12" rx="1.5"/><path d="M8 8V6a2 2 0 0 1 2-2h10a2 2 0 0 1 2 2v10a2 2 0 0 1-2 2h-2"/></svg>'
const ICON_CLOSE =
  '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" aria-hidden="true"><path d="M6 6l12 12M18 6L6 18"/></svg>'

// 标题栏是窗口 chrome：必须压在所有应用模态层之上，否则弹窗打开时按钮点不动。
// 样式由网关下发 /static/electron-controls.css（CSP style-src 'self' 禁止内联）；
// 浏览器场景没有 .ada-electron-controls 元素，该文件是空操作。
function installSsrWindowControls() {
  if (document.getElementById('ada-electron-window-controls')) return
  const topbar = document.querySelector('.ada-topbar')
  if (!topbar) return
  const actions = topbar.querySelector('.ada-topbar__actions') || topbar

  if (!document.getElementById('ada-electron-controls-style')) {
    const link = document.createElement('link')
    link.id = 'ada-electron-controls-style'
    link.rel = 'stylesheet'
    link.href = '/static/electron-controls.css'
    document.head?.appendChild(link)
  }

  const controls = document.createElement('div')
  controls.id = 'ada-electron-window-controls'
  controls.className = 'ada-electron-controls'

  const button = (action, label, title, icon, onClick) => {
    const el = document.createElement('button')
    el.type = 'button'
    el.dataset.action = action
    el.setAttribute('aria-label', label)
    el.setAttribute('title', title)
    el.innerHTML = icon
    el.addEventListener('click', onClick)
    return el
  }

  const minimize = button('minimize', '最小化', '最小化', ICON_MINIMIZE,
    () => { void ipcRenderer.invoke('desktop:window-minimize') })
  const maximize = button('maximize', '最大化', '最大化', ICON_MAXIMIZE, () => {
    void ipcRenderer.invoke('desktop:window-maximize')
    window.setTimeout(refreshMaximized, 80)
  })
  const close = button('close', '关闭窗口', '关闭窗口', ICON_CLOSE,
    () => { void ipcRenderer.invoke('desktop:window-close') })
  controls.append(minimize, maximize, close)
  actions.appendChild(controls)

  function applyMaximized(isMax) {
    maximize.innerHTML = isMax ? ICON_RESTORE : ICON_MAXIMIZE
    maximize.setAttribute('aria-label', isMax ? '向下还原' : '最大化')
    maximize.setAttribute('title', isMax ? '向下还原' : '最大化')
  }
  function refreshMaximized() {
    void ipcRenderer.invoke('desktop:window-is-maximized').then(applyMaximized)
  }
  ipcRenderer.on('window:maximize-change', (_event, isMax) => applyMaximized(isMax === true))
  refreshMaximized()
}

if (document.readyState === 'loading') {
  document.addEventListener('DOMContentLoaded', installSsrWindowControls, { once: true })
} else {
  installSsrWindowControls()
}
// 整页表单提交后会换文档；preload 会重新执行，再兜一次 pageshow。
window.addEventListener('pageshow', installSsrWindowControls)
