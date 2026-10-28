param(
    [switch]$Package
)

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
$BuildPath = Join-Path $Root 'build'
if (Test-Path $BuildPath) {
    Get-ChildItem -Force $BuildPath |
        Where-Object { $_.Name -notin @('gguf', 'llamacpp') } |
        Remove-Item -Recurse -Force
}
New-Item -ItemType Directory -Path (Join-Path $Root 'build/html'), (Join-Path $Root 'build/history'), (Join-Path $Root 'build/log') | Out-Null
go -C backend build -trimpath -ldflags='-s -w' -o (Join-Path $Root 'build/EasyWork.exe') .
if ($LASTEXITCODE -ne 0) { throw 'Go build failed' }

Write-Host '[4/4] Copying static pages and local dictionaries'
Copy-Item -Recurse -Force (Join-Path $Root 'frontend/dist/*') (Join-Path $Root 'build/html')
Write-Host "Build complete: $Root\build\EasyWork.exe"

if ($Package) {
    Write-Host '[5/5] Creating EasyWork.7z with maximum compression'
    $SevenZipCommand = Get-Command 7z -ErrorAction SilentlyContinue
    $SevenZip = if ($SevenZipCommand) {
        $SevenZipCommand.Source
    } else {
        Join-Path ${env:ProgramFiles} '7-Zip\7z.exe'
    }
    if (-not (Test-Path $SevenZip)) { throw '7-Zip was not found. Install it or add 7z.exe to PATH.' }

    $ArchivePath = Join-Path $Root 'EasyWork.7z'
    if (Test-Path $ArchivePath) { Remove-Item -Force $ArchivePath }
    Push-Location $BuildPath
    try {
        & $SevenZip a -t7z -mx=9 $ArchivePath EasyWork.exe html history
        if ($LASTEXITCODE -ne 0) { throw '7-Zip packaging failed' }
    } finally {
        Pop-Location
    }
    Write-Host "Package complete: $ArchivePath"
}
