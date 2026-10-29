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

const { app, BrowserWindow, ipcMain, dialog, screen, shell, Tray, Menu, nativeImage } = require('electron')
const path = require('path')
const fs = require('fs')
const crypto = require('crypto')
const { spawn, execSync, execFileSync } = require('child_process')
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
let tray = null

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

// ── 网关自动准备（开发期自动构建）──────────────────────────────────────
// 壳启动不再依赖先跑 make：开发模式下每次启动都 go build 到 desktop/bin
// （与 make desktop 同路径），保证运行的就是当前源码。打包形态只用随包
// resources/bin/adag，不构建。关闭自动构建：ADAGO_GATEWAY_AUTOBUILD=0。
const GATEWAY_AUTOBUILD = process.env.ADAGO_GATEWAY_AUTOBUILD !== '0'
const BUILD_TIMEOUT_MS = 180000
const SPAWN_TIMEOUT_MS = 15000
const READY_TIMEOUT_MS = 180000

function executableName() {
  return process.platform === 'win32' ? 'adag.exe' : 'adag'
}

function findRepoRoot() {
  const candidates = [
    path.resolve(app.getAppPath(), '..'),
    process.cwd(),
    path.resolve(process.cwd(), '..'),
    path.dirname(process.execPath),
    path.resolve(path.dirname(process.execPath), '..'),
  ]
  for (const dir of candidates) {
    try {
      if (fs.existsSync(path.join(dir, 'go.mod')) && fs.existsSync(path.join(dir, 'cmd', 'adag'))) return dir
    } catch { /* 候选不可读就试下一个 */ }
  }
  return null
}

function runLoggedCommand(cwd, command, args, tag, timeoutMs) {
  return new Promise((resolve) => {
    const env = { ...process.env, GOWORK: 'off', CGO_ENABLED: '0' }
    let child
    try {
      child = spawn(command, args, { cwd, env, windowsHide: true, stdio: ['ignore', 'pipe', 'pipe'] })
    } catch (err) {
      appendLauncherLog(`[${tag}] 无法启动 ${command}: ${err.message}`)
      resolve(false)
      return
    }
    const timer = setTimeout(() => {
      appendLauncherLog(`[${tag}] 超时（${timeoutMs}ms），终止`)
      try { child.kill() } catch { /* 已退出 */ }
      resolve(false)
    }, timeoutMs)
    const logPipe = (stream, which) => {
      if (!stream) return
      readline.createInterface({ input: stream }).on('line', (line) => {
        appendLauncherLog(`[${tag} ${which}] ${line}`)
      })
    }
    logPipe(child.stdout, 'stdout')
    logPipe(child.stderr, 'stderr')
    child.on('error', (err) => {
      clearTimeout(timer)
      appendLauncherLog(`[${tag}] 失败: ${err.message}`)
      resolve(false)
    })
    child.on('exit', (code) => {
      clearTimeout(timer)
      appendLauncherLog(`[${tag}] 退出 code=${code}`)
      resolve(code === 0)
    })
  })
}

function runGoBuild(repoRoot, args) {
  return runLoggedCommand(repoRoot, 'go', args, '自动构建', BUILD_TIMEOUT_MS)
}

// 开发期把官方插件的 index.ts 重新擦成 dist/index.js，保证 go:embed 拿到最新后端代码。
// 失败不阻断启动：go build 仍会嵌入上一次的 dist，并在日志里给出警告。
async function runOfficialPluginBuild(repoRoot) {
  const script = path.join(repoRoot, 'plugins', 'official', 'build.mjs')
  if (!fs.existsSync(script)) return true
  const ok = await runLoggedCommand(repoRoot, 'node', [script], '插件构建', 120000)
  if (!ok) appendLauncherLog('[插件构建] 失败，继续使用现有 dist/index.js（先检查 node 版本是否支持 stripTypeScriptTypes）')
  return ok
}

// 返回可用的网关二进制路径；开发检出里每次启动都重新构建。
async function ensureGatewayBinary(customBin) {
  const existing = findBinary(customBin)
  if (!GATEWAY_AUTOBUILD) return existing
  if (app.isPackaged) return existing // 打包形态：只用随包二进制
  if (customBin && customBin.trim()) return existing // 显式指定就尊重用户选择

  const repoRoot = findRepoRoot()
  if (!repoRoot) return existing // 不在源码检出里：只能用已有产物

  const outPath = path.join(__dirname, 'bin', executableName())
  // 开发模式不依赖 mtime 判断：每次启动都构建，保证二进制与当前源码/嵌入插件一致。
  // go 的构建缓存会让未变化的部分很快；调用方要的是「所见即所得」。
  appendLauncherLog(`[自动构建] 开发模式：每次启动重新构建网关（cwd=${repoRoot}）`)
  // 先刷新后端插件产物（index.ts → dist/index.js），go build 才能嵌入最新代码。
  await runOfficialPluginBuild(repoRoot)

  let revision = ''
  try {
    revision = execFileSync('git', ['rev-parse', '--short', 'HEAD'], { cwd: repoRoot, encoding: 'utf-8' }).trim()
  } catch { /* 非 git 检出：版本号留空 */ }
  const buildTime = new Date().toISOString().replace(/\.\d{3}Z$/, 'Z')
  const ldflags = [
    `-X github.com/adag/ada-go/internal/version.Revision=${revision || 'dev'}`,
    `-X github.com/adag/ada-go/internal/version.BuildTime=${buildTime}`,
  ].join(' ')

  try { fs.mkdirSync(path.dirname(outPath), { recursive: true }) } catch { /* 目录已存在 */ }
  const ok = await runGoBuild(repoRoot, ['build', '-trimpath', '-ldflags', ldflags, '-o', outPath, './cmd/adag'])
  if (ok && fs.existsSync(outPath)) {
    appendLauncherLog(`[自动构建] 完成: ${outPath}`)
    return outPath
  }
  appendLauncherLog('[自动构建] 失败，回退到已有产物（若有）')
  return existing
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
  coreState.childPid = null
  coreState.port = 0
  coreState.url = ''
  coreState.endpoint = ''
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

  const gatewayBin = await ensureGatewayBinary(cli['gateway-bin'] || cfg.gateway_bin_path)
  if (!gatewayBin) {
    const msg = (!app.isPackaged && findRepoRoot())
      ? '未能准备 adag 网关：自动构建失败或 go 工具链不可用。详见 ~/.adago/launcher.log；也可先手动 `make desktop`，或在设置中指定外部网关地址'
      : '未找到 adag 二进制：请先 `make release`（或 make desktop 拷贝到 desktop/bin），或在设置中指定外部网关地址'
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

  // 产品声明：打包后走 resources/products；开发期优先仓库 products/（与源码同步，
  // 避免 desktop/products 里的旧副本让新插件/导航不出现）。
  let productPath = cli.product || cfg.product_path || ''
  if (!productPath) {
    const repoRoot = findRepoRoot()
    if (repoRoot) {
      const repoProduct = path.join(repoRoot, 'products', 'headless.agent.json')
      if (fs.existsSync(repoProduct)) productPath = repoProduct
    }
    if (!productPath) productPath = resolveAppPath('products', 'headless.agent.json') || ''
  }
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
    // 记下自己的网关句柄：before-quit/window-all-closed 才能按 PID 杀掉整棵进程树
    // （Windows 下 taskkill /T 会连同 Core 子进程一起结束）。
    childProcess = child
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
      if (childProcess === child) {
        childProcess = null
        if (coreState.childPid === pid) coreState.childPid = null
      }
      if (!isReady) {
        coreState.error = `网关启动失败并在就绪前退出 (code: ${code})`
        resolveReadySignal()
      }
    })

    setTimeout(() => {
      if (!isReady) {
        const seconds = Math.round(SPAWN_TIMEOUT_MS / 1000)
        coreState.error = `启动网关超时（${seconds} 秒内未收到握手行）`
        appendLauncherLog(`[超时] 未在 ${seconds} 秒内收到网关握手行`)
        resolveReadySignal()
      }
    }, SPAWN_TIMEOUT_MS)
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
  await Promise.race([readySignalPromise, new Promise((resolve) => setTimeout(resolve, READY_TIMEOUT_MS))])
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

// 网关还没就绪时先显示准备页：首次启动会自动构建 adag，可能停留十几秒。
async function loadPreparingPage(win, detail) {
  const safe = String(detail || '正在启动本地网关…').replace(/[&<>"]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c]))
  const html = `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8">
  <title>ada-go · 正在启动</title>
  <style>
    body { margin:0; height:100vh; display:grid; place-items:center; background:#18181b;
      color:#e4e4e7; font-family:-apple-system,BlinkMacSystemFont,"Segoe UI","Microsoft YaHei",sans-serif }
    .box { max-width: 30rem; padding: 2rem; text-align: center }
    h1 { font-size: 15px; font-weight: 600; margin: 0 0 .75rem }
    p { margin: 0 0 .5rem; font-size: 13px; line-height: 1.6; color:#a1a1aa }
    .dot { display:inline-block; width:7px; height:7px; margin-right:6px; border-radius:50%;
      background:#a1a1aa; animation: pulse 1.2s ease-in-out infinite }
    @keyframes pulse { 0%,100% { opacity:.25 } 50% { opacity:1 } }
  </style></head><body><div class="box">
    <h1><span class="dot"></span>正在启动网关</h1>
    <p>${safe}</p>
    <p>首次启动或代码刚更新时，壳会自动构建 adag，请稍候。</p>
  </div></body></html>`
  try {
    await win.loadURL('data:text/html;charset=utf-8,' + encodeURIComponent(html))
  } catch (err) {
    appendLauncherLog(`[警告] 准备页加载失败: ${err?.message ?? String(err)}`)
  }
}

// SSR 界面：等网关就绪后 `/?token=` 登录——网关校验后换发 HttpOnly 会话 cookie
// 并 303 去掉 query，令牌不留在地址栏/历史记录（与 a_da 的 SSR guard 同语义）。
async function loadSsrUi(win) {
  if (!coreState.alive) {
    await loadPreparingPage(win, '正在准备 adag gateway（缺二进制或源码有更新时会自动构建）…')
  }
  const ready = await Promise.race([
    readySignalPromise.then(() => true),
    new Promise((resolve) => setTimeout(() => resolve(false), READY_TIMEOUT_MS)),
  ])
  if (!ready || !coreState.alive || !coreState.port) {
    const reason = coreState.error || `等待网关超时（${READY_TIMEOUT_MS / 1000}s）`
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
    icon: resolveAppPath('icon.ico') || resolveAppPath('build', 'app.ico') || undefined,
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

function toggleMainWindow() {
  if (!mainWindow) return
  if (mainWindow.isVisible() && !mainWindow.isMinimized()) mainWindow.hide()
  else showMainWindow()
}

// 托盘图标：打包后在 resources/icon.ico|png；开发期在 desktop/build/app.ico|png。
function trayIconPath() {
  for (const candidate of [
    resolveAppPath('icon.ico'),
    resolveAppPath('icon.png'),
    resolveAppPath('build', 'app.ico'),
    resolveAppPath('build', 'icon.png'),
  ]) {
    if (candidate) return candidate
  }
  return null
}

// 系统托盘：窗口隐藏后仍能显示 / 打开网页 / 重启网关 / 退出。
// 图标缺失只记日志，不影响桌面端启动。
function createTray() {
  if (tray) return
  const iconPath = trayIconPath()
  if (!iconPath) {
    appendLauncherLog('[托盘] 未找到可用的托盘图标（icon.ico / icon.png / build/app.ico），跳过创建')
    return
  }
  let image = nativeImage.createFromPath(iconPath)
  if (image.isEmpty()) {
    appendLauncherLog(`[托盘] 图标读取失败: ${iconPath}`)
    return
  }
  if (process.platform === 'win32') image = image.resize({ width: 16, height: 16 })

  try {
    tray = new Tray(image)
  } catch (err) {
    appendLauncherLog(`[托盘] 创建失败: ${err?.message ?? String(err)}`)
    return
  }
  tray.setToolTip('ada-go')
  tray.setContextMenu(
    Menu.buildFromTemplate([
      { label: '显示 / 隐藏主窗口', click: () => toggleMainWindow() },
      { label: '打开网页界面', click: () => void openWebUi() },
      { type: 'separator' },
      { label: '重新唤醒网关…', click: () => void restartGatewayFromTray() },
      { type: 'separator' },
      { label: '退出 ada-go', click: () => app.quit() },
    ])
  )
  // 单击：把窗口叫到前台（不隐藏，避免用户只是想显示时反而消失）。
  tray.on('click', () => showMainWindow())
  appendLauncherLog(`[托盘] 已创建系统托盘图标（${iconPath}）`)
}

async function restartGatewayFromTray() {
  const answer = await dialog.showMessageBox({
    type: 'question',
    buttons: ['取消', '重新唤醒'],
    defaultId: 1,
    cancelId: 0,
    message: '重新唤醒网关？',
    detail: '正在执行的会话会中断；桌面端会重新加载当前界面。',
  })
  if (answer.response !== 1) return

  try {
    appendLauncherLog('[托盘] 收到「重新唤醒网关」请求')
    const report = await restartGatewaySupervisor()
    appendLauncherLog(`[托盘] 重新唤醒网关结束: alive=${report.core_alive} port=${report.core_port}`)
    if (mainWindow) await loadSsrUi(mainWindow)
  } catch (err) {
    const message = err?.message ?? String(err)
    appendLauncherLog(`[托盘] 重新唤醒网关失败: ${message}`)
    await dialog.showMessageBox({ type: 'error', message: '重新唤醒网关失败', detail: message })
  }
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
// 开发期刷新：重复 npm start 或源码变化时，按最新源码重建网关、清缓存并刷新页面。
// 保留当前页面（例如正在看的会话），只有端口变化或当前是准备页时才回首页。
let refreshingToLatest = false
function sameOriginPageUrl(previousUrl) {
  try {
    const prev = new URL(previousUrl)
    const base = new URL(coreState.endpoint || `http://127.0.0.1:${coreState.port}`)
    if (!coreState.alive || prev.origin !== base.origin) return null
    return prev.toString()
  } catch {
    return null
  }
}

async function refreshToLatest(reason = '重复启动') {
  showMainWindow()
  if (app.isPackaged) return
  if (refreshingToLatest) return
  refreshingToLatest = true
  const previousUrl = mainWindow ? mainWindow.webContents.getURL() : ''
  try {
    appendLauncherLog(`[热更新] ${reason}：按最新源码重建并刷新`)
    if (mainWindow) {
      try { await mainWindow.webContents.session.clearCache() } catch { /* 缓存清理失败不致命 */ }
    }
    const report = await restartGatewaySupervisor()
    appendLauncherLog(`[热更新] 刷新完成: alive=${report.core_alive} port=${report.core_port}`)
    if (mainWindow) {
      const target = sameOriginPageUrl(previousUrl)
      if (target) await mainWindow.loadURL(target)
      else await loadSsrUi(mainWindow)
    }
  } catch (err) {
    appendLauncherLog(`[热更新] 刷新失败: ${err?.message ?? String(err)}`)
  } finally {
    refreshingToLatest = false
    hotIgnoreUntil = Date.now() + 1200
  }
}

// ── 开发模式热更新（文件监听 → 重建 → 重启网关 → 原地刷新）──────────────
const DEV_WATCH = !app.isPackaged && process.env.ADAGO_DEV_WATCH !== '0'
const HOT_WATCH_DIRS = ['cmd', 'internal', 'app', 'plugins', 'products']
const HOT_WATCH_EXTS = new Set(['.go', '.ts', '.tsx', '.js', '.json', '.css', '.tmpl'])
const HOT_SKIP_PARTS = new Set(['node_modules', '.git', 'bin', 'out', 'dist-cache', '.adago', 'dist'])
let devWatchers = []
let hotTimer = null
let hotBusy = false
// 构建/刷新期间及其后 1.2s 的事件属于构建回声（写产物、同一次保存的重复事件），
// 不再排队，避免一次保存触发两轮重建。
let hotIgnoreUntil = 0

// 只监听会进入网关（含 go:embed）的源码；dist/、desktop/bin 等构建产物必须排除，
// 否则 build.mjs / go build 写产物会再次触发 watcher，形成重建循环。
function isHotSource(rel) {
  const p = String(rel || '').replace(/\\/g, '/')
  if (!p || p.startsWith('desktop/')) return false
  if (!HOT_WATCH_DIRS.some((dir) => p === dir || p.startsWith(dir + '/')) && p !== 'go.mod' && p !== 'go.sum') return false
  if (p.split('/').some((part) => HOT_SKIP_PARTS.has(part))) return false
  if (p.endsWith('_test.go')) return false
  return HOT_WATCH_EXTS.has(path.extname(p)) || p === 'go.mod' || p === 'go.sum'
}

function scheduleHotReload(filename) {
  if (!DEV_WATCH) return
  if (refreshingToLatest || hotBusy) return
  if (Date.now() < hotIgnoreUntil) return
  if (hotTimer) clearTimeout(hotTimer)
  hotTimer = setTimeout(() => {
    hotTimer = null
    void runHotReload(filename)
  }, 700)
}

async function runHotReload(filename) {
  if (refreshingToLatest || hotBusy) return
  hotBusy = true
  try {
    appendLauncherLog(`[热更新] 检测到变化: ${filename || '(unknown)'}`)
    await refreshToLatest('源码变化')
  } finally {
    hotBusy = false
    hotIgnoreUntil = Date.now() + 1200
  }
}

function startDevWatcher() {
  if (!DEV_WATCH) {
    appendLauncherLog('[热更新] 已关闭（ADAGO_DEV_WATCH=0 或打包形态）')
    return
  }
  const repoRoot = findRepoRoot()
  if (!repoRoot) {
    appendLauncherLog('[热更新] 不在源码检出里，跳过')
    return
  }
  const addDir = (dir) => {
    const full = path.join(repoRoot, dir)
    try {
      if (!fs.existsSync(full)) return
      const watcher = fs.watch(full, { recursive: true }, (_event, filename) => {
        const rel = path.posix.join(dir, String(filename || '').replace(/\\/g, '/'))
        if (isHotSource(rel)) scheduleHotReload(rel)
      })
      devWatchers.push(watcher)
    } catch (err) {
      appendLauncherLog(`[热更新] 无法监听 ${dir}: ${err.message}`)
    }
  }
  for (const dir of HOT_WATCH_DIRS) addDir(dir)
  // go.mod/go.sum 在仓库根，单独非递归监听（避免把 desktop/node_modules 卷进来）。
  try {
    const rootWatcher = fs.watch(repoRoot, {}, (_event, filename) => {
      const rel = String(filename || '').replace(/\\/g, '/')
      if (isHotSource(rel)) scheduleHotReload(rel)
    })
    devWatchers.push(rootWatcher)
  } catch (err) {
    appendLauncherLog(`[热更新] 无法监听仓库根目录: ${err.message}`)
  }
  appendLauncherLog(`[热更新] 监听源码目录: ${repoRoot}`)
}

function stopDevWatcher() {
  if (hotTimer) {
    clearTimeout(hotTimer)
    hotTimer = null
  }
  for (const watcher of devWatchers) {
    try { watcher.close() } catch { /* 已关闭 */ }
  }
  devWatchers = []
}

app.on('second-instance', () => { void refreshToLatest('重复启动') })
app.on('before-quit', () => {
  stopDevWatcher()
  cleanupChildProcess()
  tray?.destroy()
  tray = null
})
app.on('will-quit', () => cleanupChildProcess())
app.on('window-all-closed', () => {
  // macOS 惯例外：关窗不退出应用，网关继续服务；真正退出时由 before-quit 清理。
  if (process.platform === 'darwin') return
  cleanupChildProcess()
  app.quit()
})

app.whenReady().then(async () => {
  registerIpcHandlers()
  startGatewaySupervisor()
  const smoke = process.env.ADAGO_DESKTOP_SMOKE === '1'
  await createMainWindow()
  createTray()
  if (smoke) {
    await Promise.race([readySignalPromise, new Promise((resolve) => setTimeout(resolve, READY_TIMEOUT_MS))])
    const line = `[smoke] alive=${coreState.alive} endpoint=${coreState.endpoint || '(none)'} port=${coreState.port} error=${coreState.error ?? 'none'}`
    appendLauncherLog(line)
    process.stdout.write(line + '\n')
    setTimeout(() => app.quit(), 500)
    return
  }
  startDevWatcher()
  app.on('activate', async () => {
    if (BrowserWindow.getAllWindows().length === 0) await createMainWindow()
  })
})
