# build-mcp.ps1 - build the MCP-side binaries that ada-go can launch.
#
#   browser-use  -> mcp_projects/browser_use/bin/browser-use.exe
#   computer-use -> mcp_projects/computer_use/bin/computer-use.exe
#
# Both directories are self-contained Go modules (GOWORK=off). browser-use is a
# Windows GUI app, so it is linked with the windowsgui subsystem; computer-use is
# kept as a console binary because `computer-use mcp` speaks JSON-RPC on stdio.
#
# Usage (PowerShell 5.1+):
#   powershell -NoProfile -ExecutionPolicy Bypass -File scripts/build-mcp.ps1
#   powershell -NoProfile -ExecutionPolicy Bypass -File scripts/build-mcp.ps1 -Register
#   powershell -NoProfile -ExecutionPolicy Bypass -File scripts/build-mcp.ps1 -Register -Scope global
#   powershell -NoProfile -ExecutionPolicy Bypass -File scripts/build-mcp.ps1 -Register -IncludeBrowserUseMcp
#
# -Register writes a stdio server entry into .adago/mcp.json (workspace by
# default) so the official mcp-client plugin starts it. Without -Register the
# script only builds the binaries and prints the entry that would be used.
[CmdletBinding()]
param(
  [switch]$Register,
  [ValidateSet("workspace", "global")]
  [string]$Scope = "workspace",
  [switch]$IncludeBrowserUseMcp
)

$ErrorActionPreference = "Stop"

$repo = Split-Path -Parent $PSScriptRoot
$isWindows = [System.IO.Path]::DirectorySeparatorChar -eq '\'
$exeSuffix = if ($isWindows) { ".exe" } else { "" }

$go = Get-Command go -ErrorAction SilentlyContinue
if (-not $go) {
  throw "go was not found in PATH; Go 1.27.3+ is required to build the MCP binaries."
}

$targets = @(
  [pscustomobject]@{
    Name   = "browser-use"
    Dir    = Join-Path $repo "mcp_projects\browser_use"
    Output = Join-Path $repo ("mcp_projects\browser_use\bin\browser-use" + $exeSuffix)
    Gui    = $true
  },
  [pscustomobject]@{
    Name   = "computer-use"
    Dir    = Join-Path $repo "mcp_projects\computer_use"
    Output = Join-Path $repo ("mcp_projects\computer_use\bin\computer-use" + $exeSuffix)
    Gui    = $false
  }
)

$oldGoWork = $env:GOWORK
$oldCgo = $env:CGO_ENABLED
$env:GOWORK = "off"
$env:CGO_ENABLED = "0"
try {
  foreach ($target in $targets) {
    if (-not (Test-Path -LiteralPath (Join-Path $target.Dir "go.mod"))) {
      throw "missing module: $(Join-Path $target.Dir 'go.mod')"
    }
    $binDir = Split-Path -Parent $target.Output
    New-Item -ItemType Directory -Force -Path $binDir | Out-Null
    Write-Host ("building {0} -> {1}" -f $target.Name, $target.Output)

    $buildArgs = @("-C", $target.Dir, "build", "-trimpath")
    if ($target.Gui -and $isWindows) {
      $buildArgs += @("-ldflags", "-H windowsgui")
    }
    $buildArgs += @("-o", $target.Output, ".")
    & $go.Source @buildArgs
    if ($LASTEXITCODE -ne 0) {
      throw ("go build failed for {0} (exit {1})" -f $target.Name, $LASTEXITCODE)
    }
    $item = Get-Item -LiteralPath $target.Output
    Write-Host ("  ok: {0} ({1:N0} bytes)" -f $item.Name, $item.Length)
  }
} finally {
  $env:GOWORK = $oldGoWork
  $env:CGO_ENABLED = $oldCgo
}

$browserUseExe = $targets[0].Output
$computerUseExe = $targets[1].Output

function Read-McpConfig([string]$Path) {
  $doc = [ordered]@{ version = 1; servers = @() }
  if (-not (Test-Path -LiteralPath $Path)) {
    return $doc
  }
  $raw = Get-Content -LiteralPath $Path -Raw -Encoding UTF8
  if ([string]::IsNullOrWhiteSpace($raw)) {
    return $doc
  }
  try {
    $parsed = $raw | ConvertFrom-Json
  } catch {
    throw ("{0} is not valid JSON; fix or remove it before using -Register" -f $Path)
  }
  if ($null -ne $parsed -and $parsed.PSObject.Properties.Name -contains "version") {
    $doc.version = $parsed.version
  }
  if ($null -ne $parsed -and $parsed.PSObject.Properties.Name -contains "servers" -and $null -ne $parsed.servers) {
    $doc.servers = @($parsed.servers)
  }
  return $doc
}

function Upsert-Server($servers, $entry) {
  $list = [System.Collections.ArrayList]::new()
  foreach ($item in @($servers)) {
    if ($null -ne $item) { [void]$list.Add($item) }
  }
  for ($i = 0; $i -lt $list.Count; $i++) {
    if ($list[$i].id -eq $entry.id) {
      $list[$i] = $entry
      return $list.ToArray()
    }
  }
  [void]$list.Add($entry)
  return $list.ToArray()
}

if ($Register) {
  if ($Scope -eq "global") {
    $homeDir = $env:USERPROFILE
    if (-not $homeDir) { $homeDir = $env:HOME }
    if (-not $homeDir) { throw "cannot resolve the user home directory for -Scope global" }
    $configPath = Join-Path (Join-Path $homeDir ".adago") "mcp.json"
  } else {
    $configPath = Join-Path (Join-Path $repo ".adago") "mcp.json"
  }

  $doc = Read-McpConfig $configPath
  $servers = $doc.servers

  $computerEntry = [ordered]@{
    id        = "computer_use"
    transport = "stdio"
    command   = [System.IO.Path]::GetFullPath($computerUseExe)
    args      = @("mcp", "--spawn")
    enabled   = $true
  }
  $servers = Upsert-Server $servers $computerEntry

  if ($IncludeBrowserUseMcp) {
    $browserEntry = [ordered]@{
      id        = "browser_use"
      transport = "stdio"
      command   = [System.IO.Path]::GetFullPath($browserUseExe)
      args      = @("mcp")
      enabled   = $true
    }
    $servers = Upsert-Server $servers $browserEntry
  }

  $doc.servers = @($servers)
  $json = ConvertTo-Json -InputObject $doc -Depth 10
  $json = $json + [Environment]::NewLine
  $configDir = Split-Path -Parent $configPath
  New-Item -ItemType Directory -Force -Path $configDir | Out-Null
  $utf8 = New-Object System.Text.UTF8Encoding($false)
  [System.IO.File]::WriteAllText($configPath, $json, $utf8)
  Write-Host ("registered MCP servers in {0}" -f $configPath)
} else {
  $computerCommand = [System.IO.Path]::GetFullPath($computerUseExe)
  Write-Host ""
  Write-Host "ada-go integration:"
  Write-Host ("  browser-use  : {0}" -f $browserUseExe)
  Write-Host "                 auto-discovered by the official browser-use plugin; no MCP entry needed."
  Write-Host ("  computer-use : {0}" -f $computerUseExe)
  Write-Host "                 add this to .adago/mcp.json (or rerun with -Register):"
  Write-Host ('                 {"id":"computer_use","transport":"stdio","command":"' + ($computerCommand -replace '\\', '\\') + '","args":["mcp","--spawn"],"enabled":true}')
}
