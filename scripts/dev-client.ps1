# 以开发模式运行客户端（前端热重载）
[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
Push-Location $root
try {
  Write-Host '==> wails dev' -ForegroundColor Cyan
  wails dev
} finally { Pop-Location }
