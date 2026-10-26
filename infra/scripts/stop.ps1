# 停止 launch.ps1 启动的全部内容：三个成员窗口、Web 客户端、三个节点与 Anvil 链。
#   Anvil 不保存状态：停止后链上数据丢失，下次启动会新建实验网络。
param([switch]$NoPause)
$root = (Resolve-Path (Join-Path $PSScriptRoot '../..')).Path
$data = Join-Path $env:LOCALAPPDATA 'GitChain'
$Host.UI.RawUI.WindowTitle = 'GitChain 停止'

Write-Host '== 关闭成员窗口' -ForegroundColor Cyan
Get-CimInstance Win32_Process -Filter "Name='chrome.exe' OR Name='msedge.exe'" |
  Where-Object { $_.CommandLine -like "*$data\browser-*" } |
  ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }

Write-Host '== 停止 Web 客户端（5173）' -ForegroundColor Cyan
Get-NetTCPConnection -LocalPort 5173 -State Listen -ErrorAction SilentlyContinue |
  ForEach-Object { & taskkill.exe /PID $_.OwningProcess /T /F 2>$null | Out-Null }
# The launcher's hidden "cmd /c npm run dev" wrapper, if vite already exited.
Get-CimInstance Win32_Process -Filter "Name='cmd.exe'" |
  Where-Object { $_.CommandLine -like '*npm --prefix web run dev*' } |
  ForEach-Object { & taskkill.exe /PID $_.ProcessId /T /F 2>$null | Out-Null }

Write-Host '== 停止节点与 Anvil' -ForegroundColor Cyan
$git = (Get-Command git -ErrorAction SilentlyContinue).Source
$bash = $null
if ($git) { foreach ($rel in '..\..\bin\bash.exe', '..\..\..\bin\bash.exe') { $c = Join-Path $git $rel; if (Test-Path $c) { $bash = $c; break } } }
if ($bash) { & $bash (Join-Path $root 'infra/scripts/devnet.sh') down }
# 兜底：按端口结束残留进程
foreach ($p in 8545, 8081, 8082, 8083) {
  Get-NetTCPConnection -LocalPort $p -State Listen -ErrorAction SilentlyContinue |
    ForEach-Object { Stop-Process -Id $_.OwningProcess -Force -ErrorAction SilentlyContinue }
}
Write-Host '已全部停止。' -ForegroundColor Green
if (-not $NoPause) { Read-Host '按回车关闭' | Out-Null }
