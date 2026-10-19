# Run from any directory. Does not reset the chain or write to room repositories.
$ErrorActionPreference = 'Stop'
$root = (Resolve-Path (Join-Path $PSScriptRoot '../..')).Path
$env:Path = "$env:USERPROFILE\sdk\go\bin;$env:USERPROFILE\sdk\foundry;" + $env:Path
Push-Location $root
try {
  & go vet ./...
  if ($LASTEXITCODE -ne 0) { throw 'go vet failed' }
  & go test -count=1 ./...
  if ($LASTEXITCODE -ne 0) { throw 'go test failed' }
  & forge test --root contracts
  if ($LASTEXITCODE -ne 0) { throw 'forge failed' }
  foreach ($part in @('verifier','web','desktop')) {
    & npm --prefix $part test
    if ($LASTEXITCODE -ne 0) { throw "$part tests failed" }
  }
  & npm --prefix web run build
  if ($LASTEXITCODE -ne 0) { throw 'web build failed' }
  Write-Host 'All checks passed. This does not test MetaMask or a production deployment.'
} finally { Pop-Location }
