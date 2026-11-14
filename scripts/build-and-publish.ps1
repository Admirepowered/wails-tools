# 构建客户端并自动发布到同步服务器（哈希更新）
[CmdletBinding()]
param(
  [string]$Server = $(if ($env:WT_SERVER) { $env:WT_SERVER } else { 's2.admirecn.de' }),
  [int]$Port = 8642,
  [string]$User = $env:WT_USER,
  [string]$Pass = $env:WT_PASS,
  [string]$Channel = 'stable',
  [string]$Notes = '',
  [string]$Version = '',
  [switch]$SkipBuild,
  [switch]$NoUDP
)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
Push-Location $root
try {
  if (-not $Version) {
    $versionFile = Join-Path $root 'internal\version\version.go'
    $match = Select-String -Path $versionFile -Pattern 'Version\s*=\s*"([^"]+)"'
    if ($match) { $Version = $match.Matches[0].Groups[1].Value } else { $Version = '0.0.0' }
  }

  if (-not $SkipBuild) {
    Write-Host '==> wails build' -ForegroundColor Cyan
    wails build
    if ($LASTEXITCODE -ne 0) { throw 'wails build failed' }
  }

  $exe = Join-Path $root 'build\bin\WailsTools.exe'
  if (-not (Test-Path $exe)) { throw "missing build output: $exe" }

  if (-not $User -or -not $Pass) {
    Write-Host '未设置 WT_USER / WT_PASS，跳过自动发布。' -ForegroundColor Yellow
    Write-Host '  示例：$env:WT_USER="wtadmin"; $env:WT_PASS="***"; ./scripts/build-and-publish.ps1' -ForegroundColor DarkGray
    return
  }

  Write-Host "==> publish v$Version to ${Server}:$Port ($Channel)" -ForegroundColor Cyan
  $args = @(
    'run', './cmd/wt-publish',
    '-server', $Server,
    '-port', "$Port",
    '-user', $User,
    '-pass', $Pass,
    '-file', $exe,
    '-version', $Version,
    '-channel', $Channel,
    '-platform', 'windows',
    '-arch', 'amd64'
  )
  if ($Notes) { $args += @('-notes', $Notes) }
  if ($NoUDP) { $args += '-no-udp' }
  & go @args
  if ($LASTEXITCODE -ne 0) { throw 'publish failed' }

  Write-Host "OK -> 已发布 v$Version，客户端默认从服务器按哈希检查更新" -ForegroundColor Green
} finally {
  Pop-Location
}
