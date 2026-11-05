# smoke-browser-use.ps1 - real WebView smoke for the browser-use module.
#
# Manual/desktop gate (not CI): starts the fixture site and an isolated
# browser-use instance, then exercises navigate/snapshot/wait/diff/screenshot
# through the loopback API. Uses a unique BROWSER_USE_APP_NAME and removes its
# %APPDATA% directory and processes afterwards.
#
# Usage:
#   powershell -NoProfile -ExecutionPolicy Bypass -File scripts/smoke-browser-use.ps1
#   ... -Exe C:\path\to\browser-use.exe -FixturePort 8765
[CmdletBinding()]
param(
  [string]$Exe = "",
  [int]$FixturePort = 8765
)

$ErrorActionPreference = "Stop"
$repo = Split-Path -Parent $PSScriptRoot
if (-not $Exe) {
  $Exe = Join-Path $repo "mcp_projects\browser_use\bin\browser-use.exe"
}
if (-not (Test-Path -LiteralPath $Exe)) {
  throw "browser-use executable not found: $Exe (build with scripts/build-mcp.ps1)"
}
$node = Get-Command node -ErrorAction SilentlyContinue
if (-not $node) {
  throw "node is required to run testdata/site/server.mjs"
}

$appName = "browser-use-smoke-$PID"
$dataDir = Join-Path $env:APPDATA $appName
$fixtureDir = Join-Path $repo "mcp_projects\browser_use\testdata\site"
$oldAppName = $env:BROWSER_USE_APP_NAME
$server = $null
$appPid = 0

function Wait-Http([string]$Url, [int]$Seconds) {
  $deadline = (Get-Date).AddSeconds($Seconds)
  while ((Get-Date) -lt $deadline) {
    try {
      $r = Invoke-WebRequest -UseBasicParsing -Uri $Url -TimeoutSec 2
      if ($r.StatusCode -eq 200) { return $true }
    } catch { }
    Start-Sleep -Milliseconds 200
  }
  return $false
}

try {
  $server = Start-Process -FilePath $node.Source `
    -ArgumentList @("server.mjs", "$FixturePort") `
    -WorkingDirectory $fixtureDir -PassThru -WindowStyle Hidden
  if (-not (Wait-Http "http://127.0.0.1:$FixturePort/" 10)) {
    throw "fixture site did not start on port $FixturePort"
  }

  $env:BROWSER_USE_APP_NAME = $appName
  Start-Process -FilePath $Exe -ArgumentList @("cli", "start") -PassThru | Out-Null

  $rtPath = Join-Path $dataDir "runtime.json"
  $deadline = (Get-Date).AddSeconds(30)
  $rt = $null
  while ((Get-Date) -lt $deadline) {
    if (Test-Path -LiteralPath $rtPath) {
      try {
        $rt = Get-Content -LiteralPath $rtPath -Raw | ConvertFrom-Json
        if ($rt.port -and (Wait-Http "http://127.0.0.1:$($rt.port)/v1/health" 2)) { break }
      } catch { }
    }
    Start-Sleep -Milliseconds 250
  }
  if (-not $rt -or -not $rt.port) { throw "browser-use did not publish runtime.json" }
  $appPid = [int]$rt.pid
  $base = "http://127.0.0.1:$($rt.port)"
  $headers = @{ Authorization = "Bearer $($rt.token)"; "Content-Type" = "application/json" }

  function Invoke-Tool([string]$Name, $ToolArgs) {
    $body = @{ name = $Name; args = $ToolArgs } | ConvertTo-Json -Depth 12
    $res = Invoke-RestMethod -Method Post -Uri "$base/v1/call" -Headers $headers -Body $body -TimeoutSec 60
    if (-not $res.ok) { throw ("tool $Name failed: " + ($res.error | ConvertTo-Json -Compress)) }
    return $res
  }

  Invoke-Tool "navigate" @{ url = "http://127.0.0.1:$FixturePort/" } | Out-Null
  $snap = Invoke-Tool "snapshot" @{ max_chars = 12000 }
  if ($snap.data.snapshot -notmatch 'link') { throw "snapshot did not contain a link" }

  # Missing selector + state=hidden must match immediately, not time out.
  $hidden = Invoke-Tool "wait" @{ for = "selector"; value = "#no-such-element"; state = "hidden"; timeout_ms = 3000 }
  if (-not $hidden.data.matched) { throw "wait selector state=hidden did not match" }

  # Diff mode: inject a clickable div and expect a "+" line.
  Invoke-Tool "eval" @{ expression = "document.body.innerHTML='<div id=card style=""cursor:pointer"">Smoke</div>'; 'ok'" } | Out-Null
  $diff = Invoke-Tool "snapshot" @{ diff = $true }
  if ($diff.data.snapshot -notmatch '\+ \[e\d+\] clickable') { throw "diff snapshot did not report an addition" }

  # SPAs: pushState must bump nav and wait for=url must read location.href.
  Invoke-Tool "eval" @{ expression = "history.pushState({}, '', '/smoke-route'); 'ok'" } | Out-Null
  $urlWait = Invoke-Tool "wait" @{ for = "url"; value = "/smoke-route"; timeout_ms = 2000 }
  if (-not $urlWait.data.matched) { throw "wait for=url did not see pushState" }

  # text= locator: the prefiltered rewrite must keep finding "Submit" by text.
  Invoke-Tool "navigate" @{ url = "http://127.0.0.1:$FixturePort/form" } | Out-Null
  $click = Invoke-Tool "click" @{ text = "Submit" }
  if ("$($click.data.url)" -notmatch '/done') { throw "click by text did not reach /done: $($click.data.url)" }

  # network capture + in-page wait: netRecord resolves waiters, no Go polling.
  Invoke-Tool "navigate" @{ url = "http://127.0.0.1:$FixturePort/auto" } | Out-Null
  $nwait = Invoke-Tool "network" @{ action = "wait"; url_contains = "/api/schedule"; timeout_ms = 5000 }
  if (-not $nwait.data.entry -or [int]$nwait.data.entry.status -ne 200) { throw "network wait did not catch the auto fetch" }
  $nlist = Invoke-Tool "network" @{ action = "list"; url_contains = "/api/" }
  if (-not $nlist.data.entries -or $nlist.data.entries.Count -lt 1) { throw "network list lost the captured request" }

  # read formats: tables render as markdown, links list anchors.
  Invoke-Tool "navigate" @{ url = "http://127.0.0.1:$FixturePort/" } | Out-Null
  $links = Invoke-Tool "read" @{ format = "links" }
  if ("$($links.data.content)" -notmatch 'form') { throw "read format=links lost the form link" }

  # full-page capture: 3200px page with a fixed top bar, stitched from tiles.
  Invoke-Tool "navigate" @{ url = "http://127.0.0.1:$FixturePort/long" } | Out-Null
  $full = Invoke-Tool "screenshot" @{ annotate = $false; inline = $false; full_page = $true }
  if (-not (Test-Path -LiteralPath $full.data.path)) { throw "full-page screenshot file missing" }
  if ([double]$full.data.css_height -lt 2500) { throw "full-page css_height too small: $($full.data.css_height)" }
  if ([int]$full.data.tiles -lt 3) { throw "full-page tiles too few: $($full.data.tiles)" }

  $shot = Invoke-Tool "screenshot" @{ annotate = $false; inline = $false; format = "jpeg"; quality = 70 }
  if (-not (Test-Path -LiteralPath $shot.data.path)) { throw "screenshot file missing: $($shot.data.path)" }

  # drag: HTML5 DnD onto the bin, then a mousemove-driven reorder.
  Invoke-Tool "navigate" @{ url = "http://127.0.0.1:$FixturePort/drag" } | Out-Null
  Invoke-Tool "drag" @{ from_text = "Draggable card"; to_text = "Drop bin"; payload = "card" } | Out-Null
  $dnd = Invoke-Tool "eval" @{ expression = "document.getElementById('dnd-result').textContent" }
  if ("$($dnd.data.result)" -notmatch 'dropped card') { throw "HTML5 drag did not drop: $($dnd.data.result)" }
  Invoke-Tool "drag" @{ from_text = "Item A"; to_text = "Item B" } | Out-Null
  $ptr = Invoke-Tool "eval" @{ expression = "document.getElementById('ptr-result').textContent" }
  if ("$($ptr.data.result)" -notmatch 'item-b,item-a') { throw "pointer drag did not reorder: $($ptr.data.result)" }

  # storage: set/get/list/clear on the current origin.
  Invoke-Tool "storage" @{ action = "set"; key = "bu-smoke"; value = "hello" } | Out-Null
  $sget = Invoke-Tool "storage" @{ action = "get"; key = "bu-smoke" }
  if ("$($sget.data.value)" -notmatch 'hello') { throw "storage get lost the value" }
  $slist = Invoke-Tool "storage" @{ action = "list" }
  if ([int]$slist.data.count -lt 1) { throw "storage list empty" }
  Invoke-Tool "storage" @{ action = "clear"; key = "bu-smoke" } | Out-Null

  # nested frames: /nested embeds /frames which embeds /form (f1.1).
  Invoke-Tool "navigate" @{ url = "http://127.0.0.1:$FixturePort/nested" } | Out-Null
  $frames = Invoke-Tool "frames" @{ action = "list" }
  $fids = ($frames.data.frames | ForEach-Object { $_.id }) -join ","
  if ($fids -notmatch 'f1\.1') { throw "nested frame f1.1 missing: $fids" }

  # fast path: PNG + no annotate + no scale, capture bytes hit disk as-is.
  $shotFast = Invoke-Tool "screenshot" @{ annotate = $false; inline = $true; format = "png" }
  if (-not $shotFast.data.data) { throw "fast-path inline screenshot has no data" }

  Write-Host "smoke-browser-use: ok (version $($rt.version), port $($rt.port))"
  Set-Content -LiteralPath "$env:TEMP\smoke-result.txt" -Value "ok"
} catch {
  Set-Content -LiteralPath "$env:TEMP\smoke-result.txt" -Value ("FAIL: " + $_.Exception.Message)
  throw
} finally {
  if ($appPid -gt 0) { Stop-Process -Id $appPid -Force -ErrorAction SilentlyContinue }
  if ($server -and -not $server.HasExited) {
    Stop-Process -Id $server.Id -Force -ErrorAction SilentlyContinue
  }
  # Start-Process node may be a shim that exits while its child keeps serving
  # the fixture port; a stale server would shadow later runs with old routes.
  # Kill whoever still listens on the fixture port.
  try {
    Get-NetTCPConnection -LocalPort $FixturePort -State Listen -ErrorAction SilentlyContinue |
      ForEach-Object { Stop-Process -Id $_.OwningProcess -Force -ErrorAction SilentlyContinue }
  } catch { }
  if ($oldAppName) { $env:BROWSER_USE_APP_NAME = $oldAppName } else { Remove-Item Env:BROWSER_USE_APP_NAME -ErrorAction SilentlyContinue }
  Start-Sleep -Milliseconds 300
  if ((Test-Path -LiteralPath $dataDir) -and ((Resolve-Path -LiteralPath $dataDir).Path -eq (Join-Path $env:APPDATA $appName))) {
    Remove-Item -LiteralPath $dataDir -Recurse -Force -ErrorAction SilentlyContinue
  }
}
