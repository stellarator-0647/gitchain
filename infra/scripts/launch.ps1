# GitChain 一键启动：本地实验网络 + Web 客户端 + 三个成员窗口（甲方 A / 乙方 B / 监理 C）。
#   已在运行的部分直接复用；Anvil 已停止（链已丢失）时才重建实验网络。
#   用 stop.ps1 停止。窗口里的节点设置保存在 %LOCALAPPDATA%\GitChain\browser-a|b|c。
param([switch]$NoBrowser, [switch]$NoPause)
$ErrorActionPreference = 'Stop'
$root = (Resolve-Path (Join-Path $PSScriptRoot '../..')).Path
$data = Join-Path $env:LOCALAPPDATA 'GitChain'
$env:Path = "$env:USERPROFILE\sdk\go\bin;$env:USERPROFILE\sdk\foundry;" + $env:Path
$Host.UI.RawUI.WindowTitle = 'GitChain 启动器'

function Step($t) { Write-Host "`n== $t" -ForegroundColor Cyan }
function Ok($t) { Write-Host "   $t" -ForegroundColor Green }
function Test-Port($p) {
  # Vite may listen on ::1 only (localhost), the nodes on 127.0.0.1.
  foreach ($h in '127.0.0.1', '::1') {
    $family = if ($h -match ':') { 'InterNetworkV6' } else { 'InterNetwork' }
    $c = [Net.Sockets.TcpClient]::new([Net.Sockets.AddressFamily]$family)
    try { $c.Connect($h, $p); return $true } catch { } finally { $c.Dispose() }
  }
  $false
}
function Wait-Port($p, $sec) {
  for ($i = 0; $i -lt $sec * 2; $i++) { if (Test-Port $p) { return } ; Start-Sleep -Milliseconds 500 }
  throw "等待端口 $p 超时"
}
function Find-Bash {
  $git = (Get-Command git -ErrorAction Stop).Source
  foreach ($rel in '..\..\bin\bash.exe', '..\..\..\bin\bash.exe') {
    $c = Join-Path $git $rel
    if (Test-Path $c) { return (Resolve-Path $c).Path }
  }
  throw '找不到 Git Bash（bash.exe），请安装 Git for Windows'
}
function Find-Browser {
  foreach ($c in "$env:ProgramFiles\Google\Chrome\Application\chrome.exe", "${env:ProgramFiles(x86)}\Google\Chrome\Application\chrome.exe",
                 "$env:LOCALAPPDATA\Google\Chrome\Application\chrome.exe", "${env:ProgramFiles(x86)}\Microsoft\Edge\Application\msedge.exe") {
    if (Test-Path $c) { return $c }
  }
  throw '找不到 Chrome 或 Edge'
}
function Devnet($cmd) {
  # Run in its own hidden console: Anvil and the nodes it starts must outlive this window.
  New-Item -ItemType Directory -Force $data | Out-Null
  $log = Join-Path $data "devnet-$cmd.log"
  $p = Start-Process -FilePath $script:bash -WorkingDirectory $root -WindowStyle Hidden -PassThru `
    -ArgumentList "-c `"infra/scripts/devnet.sh $cmd > '$($log -replace '\\','/')' 2>&1`""
  $p.WaitForExit()
  Get-Content $log -Encoding UTF8 | Where-Object { $_ -match '^node |wrote |created' } | ForEach-Object { Write-Host "   $_" }
  if ($p.ExitCode -ne 0) { Get-Content $log -Tail 15 | Write-Host; throw "devnet.sh $cmd 失败，完整日志：$log" }
}

try {
  Step '检查环境'
  foreach ($t in 'git', 'go', 'anvil', 'forge', 'node', 'npm') {
    if (-not (Get-Command $t -ErrorAction SilentlyContinue)) { throw "缺少 $t，请先按操作指南第 2 节安装" }
  }
  $script:bash = Find-Bash
  Ok '工具齐全'

  Step '本地实验网络（Anvil 链 + 合约 + 三个节点）'
  $nodesUp = (Test-Port 8081) -and (Test-Port 8082) -and (Test-Port 8083)
  if ((Test-Port 8545) -and $nodesUp) {
    Ok '已在运行，直接使用'
  } elseif (Test-Port 8545) {
    Write-Host '   链在运行、节点未全部启动：重启节点（保留链和数据）'
    Devnet 'restart-nodes'
  } else {
    Write-Host '   链未运行：新建实验网络（首次约 1–2 分钟，需要编译）'
    Devnet 'reset'
    Devnet 'up'
  }

  Step 'Web 客户端（http://localhost:5173）'
  if (Test-Port 5173) {
    Ok '已在运行，直接使用'
  } else {
    if (-not (Test-Path (Join-Path $root 'web/node_modules'))) {
      Write-Host '   首次运行：安装前端依赖'
      & npm --prefix (Join-Path $root 'verifier') ci; & npm --prefix (Join-Path $root 'web') ci
    }
    New-Item -ItemType Directory -Force $data | Out-Null
    $log = Join-Path $data 'web.log'
    Start-Process -WindowStyle Hidden -WorkingDirectory $root -FilePath 'cmd.exe' `
      -ArgumentList '/c', "npm --prefix web run dev -- --port 5173 --strictPort > `"$log`" 2>&1"
    Wait-Port 5173 90
    Ok "已启动（日志 $log）"
  }

  if (-not $NoBrowser) {
    Step '打开三个成员窗口'
    $browser = Find-Browser
    Add-Type -AssemblyName System.Windows.Forms
    $area = [System.Windows.Forms.Screen]::PrimaryScreen.WorkingArea
    $w = [int]($area.Width / 3)
    $i = 0
    foreach ($m in 'a', 'b', 'c') {
      $dir = Join-Path $data "browser-$m"
      $first = -not (Test-Path $dir)
      Start-Process -FilePath $browser -ArgumentList "--user-data-dir=`"$dir`"", '--no-first-run', '--no-default-browser-check',
        "--window-position=$($area.X + $i * $w),$($area.Y)", "--window-size=$w,$($area.Height)", '--app=http://localhost:5173/'
      if ($first) { Write-Host "   窗口 $($i + 1) 是新建的，请在“连接本地节点”里填写下表对应的地址和令牌（只需一次）" -ForegroundColor Yellow }
      $i++
    }
  }

  Step '就绪'
  Write-Host '   窗口      成员        节点地址                 访问令牌'
  Write-Host '   左        甲方 A      http://127.0.0.1:8081    devtoken-a   （锚定者）'
  Write-Host '   中        乙方 B      http://127.0.0.1:8082    devtoken-b'
  Write-Host '   右        监理 C      http://127.0.0.1:8083    devtoken-c'
  Write-Host '   可信部署清单：' (Join-Path $root 'deployments\31337.json')
  Write-Host '   停止：桌面“GitChain 停止”或 infra\scripts\stop.ps1' -ForegroundColor DarkGray
} catch {
  Write-Host "`n启动失败：$_" -ForegroundColor Red
  if (-not $NoPause) { Read-Host '按回车关闭' | Out-Null }
  exit 1
}
if (-not $NoPause) { Read-Host "`n按回车关闭本窗口（不会停止已启动的服务）" | Out-Null }
