$ErrorActionPreference = 'Stop'
$Root = Split-Path -Parent $MyInvocation.MyCommand.Path
Set-Location $Root

Write-Host '[1/4] Testing Go backend'
go -C backend test ./...
if ($LASTEXITCODE -ne 0) { throw 'Go tests failed' }

Write-Host '[2/4] Building offline frontend'
Set-Location (Join-Path $Root 'frontend')
npm ci
if ($LASTEXITCODE -ne 0) { throw 'npm ci failed' }
npm run build
if ($LASTEXITCODE -ne 0) { throw 'Frontend build failed' }
Set-Location $Root

Write-Host '[3/4] Building native Windows x64 executable'
$env:GOOS = 'windows'
$env:GOARCH = 'amd64'
$env:CGO_ENABLED = '0'
if (Test-Path (Join-Path $Root 'build')) { Remove-Item -Recurse -Force (Join-Path $Root 'build') }
New-Item -ItemType Directory -Path (Join-Path $Root 'build/html'), (Join-Path $Root 'build/history') | Out-Null
go -C backend build -trimpath -ldflags='-s -w' -o (Join-Path $Root 'build/EasyWork.exe') .
if ($LASTEXITCODE -ne 0) { throw 'Go build failed' }

Write-Host '[4/4] Copying static pages and local dictionaries'
Copy-Item -Recurse -Force (Join-Path $Root 'frontend/dist/*') (Join-Path $Root 'build/html')
$Forbidden = @('build/gguf', 'build/llamacpp') | Where-Object { Test-Path (Join-Path $Root $_) }
if ($Forbidden) { throw 'Release must not contain model or llama.cpp resources' }
Write-Host "Build complete: $Root\build\EasyWork.exe"
