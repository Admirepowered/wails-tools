# 构建 wt-server 服务端
[CmdletBinding()]
param([string]$Out = '')

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
if (-not $Out) { $Out = Join-Path $root 'build\bin\wt-server.exe' }
New-Item -ItemType Directory -Force -Path (Split-Path -Parent $Out) | Out-Null

Push-Location $root
try {
  Write-Host "==> go build -o $Out ./server/cmd/wt-server" -ForegroundColor Cyan
  go build -trimpath -o $Out ./server/cmd/wt-server
  if ($LASTEXITCODE -ne 0) { throw 'go build failed' }
  Write-Host "OK -> $Out" -ForegroundColor Green
} finally { Pop-Location }
