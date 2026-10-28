// ada-go 桌面壳（Electron 主进程）。
//
// 从 a_da 的 electron/main.ts 迁移（上游 MIT，commit ae2f8ffe…），差异点：
//   - 后端是 `adag gateway`：stdout 第一行就是 JSON 握手（endpoint/workspaces/token/…），
//     不再是 a_da 的 `A_DA_GATEWAY_READY <json>` 前缀行；
//   - 登录走网关的 `?token=` 自动登录（IssueSession + 303 去 query），令牌不进地址栏；
//   - 令牌由本壳生成并持久化（~/.adago/desktop_launcher.json），经
//     ADAG_GATEWAY_TOKEN 环境变量注入网关——重启网关后令牌不变，旧 cookie 不失效；
//   - 令牌/地址可以通过 CLI（--connect/--token）或配置文件接入外部网关（场景 A）；
//   - 52353 被占用时自动退到随机端口（a_da 是固定端口让浏览器好猜，桌面壳自己
//     知道端口，随机更稳；浏览器打开仍由托盘/界面拼 URL）；
//   - 本壳不创建系统托盘（ada-go 无品牌图标资产；a_da 的 logo 不随 MIT 迁移）。
//
// 环境变量 ADAGO_DESKTOP_SMOKE=1：网关就绪后打印一行 `[smoke] ...` 并退出，
// 供自动化冒烟，不弹窗口常驻。

const { app, BrowserWindow, ipcMain, dialog, screen, shell } = require('electron')
const path = require('path')
const fs = require('fs')
const crypto = require('crypto')
const { spawn, execSync } = require('child_process')
const readline = require('readline')

/** 解析随包/开发期资源路径（a_da 同款：不能信 bun/打包器硬编码的 __dirname）。 */
function resolveAppPath(...relParts) {
  const rel = path.join(...relParts)
  const candidates = [
    path.join(app.getAppPath(), rel),
    process.resourcesPath ? path.join(process.resourcesPath, rel) : '',
    path.join(__dirname, '..', rel),
    path.join(__dirname, rel),
  ].filter(Boolean)
  for (const candidate of candidates) {
    try {
      if (fs.existsSync(candidate)) return candidate
    } catch { /* 候选不可读就试下一个 */ }
  }
  return null
}

// ── 单实例 ──────────────────────────────────────────────────────────────
appendLauncherLog(`[启动] ada-go 桌面壳 argv=${process.argv.slice(1).join(' ') || '(无参数)'}`)
if (!app.requestSingleInstanceLock()) {
  appendLauncherLog('[启动] 已有实例在运行，本次启动退出')
  app.quit()
  process.exit(0)
}

let mainWindow = null

// ── CLI 参数 ────────────────────────────────────────────────────────────
function parseCliArgs() {
  const args = process.argv.slice(1)
  const out = {}
  for (let i = 0; i < args.length; i++) {
    const pair = (name) => {
      if (args[i] === '--' + name && i + 1 < args.length) { out[name] = args[++i]; return }
      if (args[i].startsWith('--' + name + '=')) out[name] = args[i].slice(name.length + 3)
    }
    pair('connect'); pair('token'); pair('workspace'); pair('gateway-bin'); pair('product')
  }
  return out
}

// ── 应用根目录与持久化配置 ──────────────────────────────────────────────
function getAppHome() {
  if (process.env.ADAGO_HOME && process.env.ADAGO_HOME.trim()) return path.resolve(process.env.ADAGO_HOME.trim())
  const userProfile = process.env.USERPROFILE || process.env.HOME || ''
  return userProfile ? path.join(userProfile, '.adago') : path.resolve('.adago')
}

function loadDesktopConfig() {
  try {
    const p = path.join(getAppHome(), 'desktop_launcher.json')
    if (fs.existsSync(p)) return JSON.parse(fs.readFileSync(p, 'utf-8'))
  } catch (err) { console.warn('[DesktopConfig] 加载失败:', err) }
  return {}
}

function saveDesktopConfig(cfg) {
  try {
    const p = path.join(getAppHome(), 'desktop_launcher.json')
    fs.mkdirSync(path.dirname(p), { recursive: true })
    fs.writeFileSync(p, JSON.stringify(cfg, null, 2), 'utf-8')
  } catch (err) { console.error('[DesktopConfig] 保存失败:', err) }
}

function appendLauncherLog(line) {
  try {
    const p = path.join(getAppHome(), 'launcher.log')
    fs.mkdirSync(path.dirname(p), { recursive: true })
    fs.appendFileSync(p, `[${new Date().toISOString()}] ${line}\n`, 'utf-8')
  } catch { /* 日志失败不致命 */ }
}

// ── 二进制探测 ──────────────────────────────────────────────────────────
function findBinary(customBin) {
  if (customBin && customBin.trim()) {
    const p = path.resolve(customBin.trim())
    if (fs.existsSync(p)) return p
  }
  const exeName = process.platform === 'win32' ? 'adag.exe' : 'adag'

  if (process.resourcesPath) {
    const inRes = path.join(process.resourcesPath, 'bin', exeName)
    if (fs.existsSync(inRes)) return inRes
  }
  const nextToExe = path.join(path.dirname(process.execPath), exeName)
  if (fs.existsSync(nextToExe)) return nextToExe

  // 开发期：仓库 bin/release（make release 产物，取最新）与 desktop/bin（make desktop 拷贝）。
  const appRoot = app.getAppPath()
  const dirs = [path.join(appRoot, 'desktop', 'bin'), path.resolve(appRoot, 'bin', 'release'), path.resolve(appRoot, 'bin')]
  for (const dir of dirs) {
    try {
      const entries = fs.readdirSync(dir).filter((f) => f.startsWith('adag') && !f.endsWith('.sha256'))
      if (entries.length > 0) {
        entries.sort((a, b) => fs.statSync(path.join(dir, b)).mtimeMs - fs.statSync(path.join(dir, a)).mtimeMs)
        return path.join(dir, entries[0])
      }
    } catch { /* 目录不存在 */ }
  }
  return null
}

// ── 网关状态与看门狗 ────────────────────────────────────────────────────
const coreState = { alive: false, port: 0, token: '', url: '', childPid: null, error: null, endpoint: '' }
let childProcess = null
let resolveReadySignal = () => {}
let readySignalPromise = new Promise((resolve) => { resolveReadySignal = resolve })

function resetReadySignal() {
  readySignalPromise = new Promise((resolve) => { resolveReadySignal = resolve })
}

function isPidAlive(pid) {
  if (!pid || pid <= 0) return false
  try { process.kill(pid, 0); return true } catch (err) { return err.code === 'EPERM' }
}

function killProcess(pid) {
  if (!pid || pid <= 0) return
  if (process.platform === 'win32') {
    try { execSync(`taskkill /pid ${pid} /f /t`, { stdio: 'ignore' }) } catch { try { process.kill(pid, 'SIGKILL') } catch {} }
  } else {
    try { process.kill(pid, 'SIGTERM') } catch {}
  }
}

function cleanupChildProcess() {
  if (childProcess && !childProcess.killed && childProcess.pid) {
    appendLauncherLog(`退出时清理网关子进程 PID: ${childProcess.pid}`)
    killProcess(childProcess.pid)
  }
  childProcess = null
  coreState.alive = false
}

function randomToken() { return crypto.randomBytes(24).toString('hex') }

// startGateway：拉起 `adag gateway` 并等待 stdout 的 JSON 握手行。
//
// 端口策略：先试固定 127.0.0.1:52353（浏览器/托盘好猜）；网关因端口被占用退出时
// 自动退到 127.0.0.1:0 随机端口重试一次。令牌走 ADAG_GATEWAY_TOKEN 环境变量，
// 由本壳生成并持久化——重启网关令牌不变。
async function startGatewaySupervisor() {
  const cli = parseCliArgs()
  const cfg = loadDesktopConfig()

  const externalUrl = cli.connect || cfg.gateway_url
  if (externalUrl) {
    appendLauncherLog(`连接至外部网关: ${externalUrl}`)
    coreState.alive = true
    coreState.url = externalUrl
    coreState.endpoint = externalUrl
    coreState.token = cli.token || cfg.token || ''
    coreState.error = null
    resolveReadySignal()
    return
  }

  const gatewayBin = findBinary(cli['gateway-bin'] || cfg.gateway_bin_path)
  if (!gatewayBin) {
    const msg = '未找到 adag 二进制：请先 `make release`（或 make desktop 拷贝到 desktop/bin），或在设置中指定外部网关地址'
    appendLauncherLog(`[错误] ${msg}`)
    dialog.showErrorBox('ada-go 启动错误', msg)
    coreState.error = msg
    resolveReadySignal()
    return
  }

  // 令牌：CLI/配置优先，否则生成一次并持久化。
  let token = cli.token || cfg.token || ''
  if (!token) {
    token = randomToken()
    saveDesktopConfig({ ...cfg, token })
  }

  // 产品声明：打包后走 resources/products；开发期走仓库 products/。
  let productPath = cli.product || cfg.product_path || resolveAppPath('products', 'headless.agent.json')
  if (productPath && !fs.existsSync(productPath)) productPath = ''

  const workspace = (cli.workspace || cfg.workspace || '').trim()
  const cwd = workspace && fs.existsSync(workspace) ? workspace : getAppHome()

  const baseEnv = { ...process.env, ADAG_GATEWAY_TOKEN: token }
  const attempt = (addr) => {
    const args = ['gateway', '--addr', addr]
    if (productPath) args.push('--product', productPath)
    if (workspace) args.push('--workspace', workspace)
    appendLauncherLog(`拉起网关: ${gatewayBin} ${args.join(' ')}`)
    const child = spawn(gatewayBin, args, { cwd, windowsHide: true, env: baseEnv, stdio: ['ignore', 'pipe', 'pipe'] })
    return child
  }
  const watch = (child, fallbackAddr) => {
    const pid = child.pid || 0
    coreState.childPid = pid
    appendLauncherLog(`网关进程已拉起, PID: ${pid}`)

    let isReady = false
    if (child.stdout) {
      const rl = readline.createInterface({ input: child.stdout })
      rl.on('line', (line) => {
        appendLauncherLog(`[adag stdout] ${line}`)
        if (isReady) return
        const trimmed = line.trim()
        if (!trimmed.startsWith('{')) return
        try {
          const payload = JSON.parse(trimmed)
          const endpoint = String(payload.endpoint || '')
          const match = endpoint.match(/:(\d+)$/)
          if (!match) return
          const port = Number(match[1])
          if (!(port > 0)) return
          isReady = true
          coreState.alive = true
          coreState.port = port
          coreState.token = token
          coreState.endpoint = `http://127.0.0.1:${port}`
          coreState.url = `http://127.0.0.1:${port}`
          coreState.error = null
          appendLauncherLog(`网关就绪: ${coreState.url}`)
          resolveReadySignal()
        } catch (e) { appendLauncherLog(`解析握手行异常: ${e}`) }
      })
    }
    if (child.stderr) {
      readline.createInterface({ input: child.stderr }).on('line', (line) => {
        appendLauncherLog(`[adag stderr] ${line}`)
      })
    }
    child.on('exit', (code, signal) => {
      appendLauncherLog(`网关退出, code: ${code}, signal: ${signal}`)
      coreState.alive = false
      if (!isReady) {
        coreState.error = `网关启动失败并在就绪前退出 (code: ${code})`
        resolveReadySignal()
      }
    })

    setTimeout(() => {
      if (!isReady) {
        coreState.error = '启动网关超时（15 秒内未收到握手行）'
        appendLauncherLog('[超时] 未在 15 秒内收到网关握手行')
        resolveReadySignal()
      }
    }, 15000)
  }

  let child = attempt('127.0.0.1:52353')
  // 固定端口被占用：网关会在就绪前退出。这里等 2.5s，没就绪且已退出 → 换随机端口重试一次。
  const earlyExit = await Promise.race([
    new Promise((resolve) => child.once('exit', () => resolve(true))),
    new Promise((resolve) => setTimeout(() => resolve(false), 2500)),
  ])
  if (earlyExit && !coreState.alive) {
    appendLauncherLog('固定端口 52353 不可用，退到随机端口重试')
    cleanupChildProcess()
    child = attempt('127.0.0.1:0')
  }
  watch(child)
}

function collectProcesses() {
  const processes = []
  if (coreState.childPid) {
    const alive = isPidAlive(coreState.childPid)
    processes.push({
      name: 'adag gateway', role: '统一网关（Core 监督/SSR/SSE）', pid: coreState.childPid,
      port: coreState.port, alive, status: alive ? 'running' : 'stopped', url: coreState.url || null,
    })
  } else if (coreState.alive) {
    processes.push({
      name: 'adag gateway (外部)', role: '外部接入网关', pid: null,
      port: coreState.port, alive: true, status: 'running', url: coreState.url || null,
    })
  }
  return processes
}

async function restartGatewaySupervisor() {
  appendLauncherLog('收到「重新唤醒网关」请求')
  cleanupChildProcess()
  coreState.alive = false; coreState.error = null; coreState.port = 0
  coreState.url = ''; coreState.endpoint = ''; coreState.childPid = null
  resetReadySignal()
  await startGatewaySupervisor()
  await Promise.race([readySignalPromise, new Promise((resolve) => setTimeout(resolve, 16000))])
  return {
    core_alive: coreState.alive, core_url: coreState.url, core_port: coreState.port,
    error: coreState.error, processes: collectProcesses(),
  }
}

// ── 窗口 ────────────────────────────────────────────────────────────────
const SSR_WINDOW_MIN_WIDTH = 450
const WINDOW_MIN_HEIGHT = 620

function resolveInitialWindowSize(minWidth = SSR_WINDOW_MIN_WIDTH) {
  const clamp = (v, lo, hi) => Math.min(Math.max(v, lo), hi)
  const { width: areaWidth, height: areaHeight } = screen.getPrimaryDisplay().workAreaSize
  const preferredWidth = Math.round(clamp(areaWidth * 0.72, 1120, 1360))
  const preferredHeight = Math.round(clamp(areaHeight * 0.78, 720, 900))
  return {
    width: Math.min(preferredWidth, Math.max(minWidth, areaWidth - 40)),
    height: Math.min(preferredHeight, Math.max(WINDOW_MIN_HEIGHT, areaHeight - 40)),
  }
}

function webUiUrl() {
  if (!coreState.alive || !coreState.port) return null
  const base = coreState.endpoint || `http://127.0.0.1:${coreState.port}`
  const u = new URL(base)
  u.pathname = '/'; u.hash = ''; u.search = ''
  if (coreState.token) u.searchParams.set('token', coreState.token)
  return u.toString()
}

async function openWebUi() {
  const url = webUiUrl()
  if (!url) {
    await dialog.showMessageBox({ type: 'info', message: '网关尚未就绪', detail: '请稍候，或从托盘/界面重启网关。' })
    return
  }
  appendLauncherLog(`在浏览器打开: ${url.split('?')[0]}`)
  await shell.openExternal(url)
}

async function loadGatewayErrorPage(win, reason) {
  const safe = reason.replace(/[&<>"]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c]))
  const html = `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8">
  <title>ada-go · 网关未就绪</title>
  <style>
    body { margin:0; height:100vh; display:grid; place-items:center; background:#18181b;
      color:#e4e4e7; font-family:-apple-system,BlinkMacSystemFont,"Segoe UI","Microsoft YaHei",sans-serif }
    .box { max-width: 30rem; padding: 2rem; text-align: center }
    h1 { font-size: 15px; font-weight: 600; margin: 0 0 .75rem }
    p { margin: 0 0 .5rem; font-size: 13px; line-height: 1.6; color:#a1a1aa }
    code { font-family: ui-monospace, Consolas, monospace; font-size: 12px; color:#d4d4d8; word-break: break-all }
  </style></head><body><div class="box">
    <h1>网关尚未就绪</h1>
    <p>ada-go 的界面由本地网关提供，现在还没连上。</p>
    <p><code>${safe}</code></p>
    <p>可以等几秒，或重启桌面壳（网关由本进程监督，会话数据都在）。</p>
  </div></body></html>`
  await win.loadURL('data:text/html;charset=utf-8,' + encodeURIComponent(html))
}

// SSR 界面：等网关就绪后 `/?token=` 登录——网关校验后换发 HttpOnly 会话 cookie
// 并 303 去掉 query，令牌不留在地址栏/历史记录（与 a_da 的 SSR guard 同语义）。
async function loadSsrUi(win) {
  const ready = await Promise.race([
    readySignalPromise.then(() => true),
    new Promise((resolve) => setTimeout(() => resolve(false), 16000)),
  ])
  if (!ready || !coreState.alive || !coreState.port) {
    const reason = coreState.error || '等待网关超时（16s）'
    appendLauncherLog(`[警告] 网关未就绪（${reason}），显示提示页`)
    await loadGatewayErrorPage(win, reason)
    return
  }
  const url = new URL(coreState.endpoint || `http://127.0.0.1:${coreState.port}`)
  url.pathname = '/'; url.hash = ''; url.search = ''
  if (coreState.token) url.searchParams.set('token', coreState.token)
  appendLauncherLog(`加载 SSR 页面: ${url.origin}${url.pathname}`)
  try {
    await win.loadURL(url.toString())
  } catch (err) {
    const reason = err?.message || String(err)
    appendLauncherLog(`[警告] SSR 页面加载失败: ${reason}`)
    await loadGatewayErrorPage(win, reason)
  }
}

async function createMainWindow() {
  const preloadPath = resolveAppPath('preload.cjs')
  if (!preloadPath) {
    const msg = '未找到 preload.cjs —— 窗口控制无法注入。请检查安装完整性。'
    appendLauncherLog(`[错误] ${msg}`)
    dialog.showErrorBox('ada-go 启动错误', msg)
  } else {
    appendLauncherLog(`preload 路径: ${preloadPath}`)
  }

  const initial = resolveInitialWindowSize()
  appendLauncherLog(`初始窗口: ${initial.width}x${initial.height}`)

  mainWindow = new BrowserWindow({
    title: 'ada-go',
    width: initial.width,
    height: initial.height,
    minWidth: SSR_WINDOW_MIN_WIDTH,
    minHeight: WINDOW_MIN_HEIGHT,
    center: true,
    frame: false, // 无边框；preload 在 .ada-topbar 注入拖拽区与窗口控制（同 a_da）。
    backgroundColor: '#18181b',
    webPreferences: {
      preload: preloadPath ?? undefined,
      contextIsolation: true,
      nodeIntegration: false,
      sandbox: false,
      webSecurity: false,
      allowRunningInsecureContent: true,
    },
  })

  // Electron ≥32 的 console-message 签名是 (event, details)；兼容两种。
  mainWindow.webContents.on('console-message', (_event, ...args) => {
    const first = args[0]
    if (args.length === 1 && first && typeof first === 'object') {
      appendLauncherLog(`[Renderer ${first.level ?? 'log'}] ${first.message ?? ''} (${first.sourceId ?? '?'}:${first.lineNumber ?? '?'})`)
      return
    }
    const [level, message, line, sourceId] = args
    appendLauncherLog(`[Renderer console ${level}] ${message} (${sourceId}:${line})`)
  })
  mainWindow.webContents.on('preload-error', (_e, p, err) => appendLauncherLog(`[preload-error] ${p}: ${err?.message ?? String(err)}`))
  mainWindow.webContents.on('render-process-gone', (_e, d) => appendLauncherLog(`[render-process-gone] reason=${d?.reason} exitCode=${d?.exitCode}`))
  mainWindow.webContents.on('did-fail-load', (_e, code, desc, u) => appendLauncherLog(`[did-fail-load] ${code}: ${desc} on ${u}`))

  mainWindow.on('maximize', () => mainWindow?.webContents.send('window:maximize-change', true))
  mainWindow.on('unmaximize', () => mainWindow?.webContents.send('window:maximize-change', false))

  mainWindow.webContents.on('before-input-event', (event, input) => {
    if ((input.control && input.shift && input.key.toLowerCase() === 'i') || input.key === 'F12') {
      mainWindow?.webContents.toggleDevTools()
      event.preventDefault()
    }
  })

  await loadSsrUi(mainWindow)
  mainWindow.on('closed', () => { mainWindow = null })
  return mainWindow
}

function showMainWindow() {
  if (!mainWindow) return
  if (mainWindow.isMinimized()) mainWindow.restore()
  mainWindow.show()
  mainWindow.focus()
}

// ── IPC（通道名与 a_da 保持一致）───────────────────────────────────────
function registerIpcHandlers() {
  ipcMain.handle('desktop:get-core-info', async () => {
    await readySignalPromise
    return {
      alive: coreState.alive, mode: 'gateway', port: coreState.port,
      token: coreState.token, url: coreState.url, error: coreState.error,
    }
  })
  ipcMain.handle('desktop:get-config', async () => loadDesktopConfig())
  ipcMain.handle('desktop:set-config', async (_e, cfg) => { saveDesktopConfig(cfg) })
  ipcMain.handle('desktop:restart', async () => {
    cleanupChildProcess()
    app.relaunch()
    app.exit(0)
  })
  ipcMain.handle('desktop:restart-gateway', async () => restartGatewaySupervisor())
  ipcMain.handle('desktop:get-process-report', async () => ({
    core_alive: coreState.alive, core_url: coreState.url, core_port: coreState.port,
    error: coreState.error, processes: collectProcesses(),
  }))
  ipcMain.handle('desktop:kill-process', async (_e, pid) => killProcess(pid))
  ipcMain.handle('desktop:window-minimize', async () => mainWindow?.minimize())
  ipcMain.handle('desktop:window-maximize', async () => {
    if (!mainWindow) return
    if (mainWindow.isMaximized()) mainWindow.unmaximize()
    else mainWindow.maximize()
  })
  ipcMain.handle('desktop:window-close', async () => mainWindow?.close())
  ipcMain.handle('desktop:window-is-maximized', async () => mainWindow?.isMaximized() ?? false)
  ipcMain.handle('desktop:open-web-ui', async () => { await openWebUi() })
}

// ── 生命周期 ────────────────────────────────────────────────────────────
app.on('second-instance', () => showMainWindow())
app.on('before-quit', () => cleanupChildProcess())
app.on('window-all-closed', () => {
  cleanupChildProcess()
  if (process.platform !== 'darwin') app.quit()
})

app.whenReady().then(async () => {
  registerIpcHandlers()
  startGatewaySupervisor()
  const smoke = process.env.ADAGO_DESKTOP_SMOKE === '1'
  await createMainWindow()
  if (smoke) {
    await Promise.race([readySignalPromise, new Promise((resolve) => setTimeout(resolve, 16000))])
    const line = `[smoke] alive=${coreState.alive} endpoint=${coreState.endpoint || '(none)'} port=${coreState.port} error=${coreState.error ?? 'none'}`
    appendLauncherLog(line)
    process.stdout.write(line + '\n')
    setTimeout(() => app.quit(), 500)
    return
  }
  app.on('activate', async () => {
    if (BrowserWindow.getAllWindows().length === 0) await createMainWindow()
  })
})
