# 构建并运行同步服务端
[CmdletBinding()]
param(
  [int]$Port = 8642,
  [string]$Data = '',
  [string]$Listen = '0.0.0.0',
  [switch]$NoUDP,
  [switch]$NoRelay,
  [switch]$DisableRegistration,
  [int]$QuotaGB = 10
)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
if (-not $Data) { $Data = Join-Path $root '.wt-server-data' }
& (Join-Path $PSScriptRoot 'build-server.ps1') | Out-Host

$exe = Join-Path $root 'build\bin\wt-server.exe'
$args = @(
  '-data', $Data,
  '-tcp', ("{0}:{1}" -f $Listen, $Port),
  '-udp', ("{0}:{1}" -f $Listen, $Port),
  '-quota-gb', $QuotaGB,
  '-allow-register=' + (-not $DisableRegistration).ToString().ToLower(),
  '-relay=' + (-not $NoRelay).ToString().ToLower()
)
if ($NoUDP) { $args += '-no-udp' }

Write-Host "==> $exe $($args -join ' ')" -ForegroundColor Cyan
& $exe @args
