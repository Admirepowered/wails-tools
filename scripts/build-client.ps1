# 构建 WailsTools 客户端（生产）
[CmdletBinding()]
param(
  [switch]$SkipTests,
  [switch]$NoClean
)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
Push-Location $root
try {
  if (-not $SkipTests) {
    Write-Host '==> go test ./...' -ForegroundColor Cyan
    go test ./...
    if ($LASTEXITCODE -ne 0) { throw 'go test failed' }
  }

  Write-Host '==> frontend type check' -ForegroundColor Cyan
  Push-Location ./frontend
  try {
    npx tsc --noEmit
    if ($LASTEXITCODE -ne 0) { throw 'tsc failed' }
  } finally { Pop-Location }

  Write-Host '==> wails build' -ForegroundColor Cyan
  $args = @('build')
  if (-not $NoClean) { $args += '-clean' }
  & wails @args
  if ($LASTEXITCODE -ne 0) { throw 'wails build failed' }

  Write-Host "OK -> $root\build\bin\WailsTools.exe" -ForegroundColor Green
} finally {
  Pop-Location
}
