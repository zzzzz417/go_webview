$ErrorActionPreference = "Stop"

$root = Split-Path -Parent $MyInvocation.MyCommand.Path
Set-Location $root

$cacheDir = Join-Path $root ".tmp-gocache"
$outDir = Join-Path $root ".tmp"
$iconDir = Join-Path $root "build\\windows"
$iconPath = Join-Path $iconDir "app.ico"
$rcPath = Join-Path $iconDir "app.rc"
$sysoPath = Join-Path $root "cmd\\server\\rsrc_windows_amd64.syso"
$exePath = Join-Path $outDir "go_webview.exe"

New-Item -ItemType Directory -Force -Path $outDir | Out-Null
New-Item -ItemType Directory -Force -Path $iconDir | Out-Null

if (-not (Test-Path $iconPath)) {
  Add-Type -AssemblyName System.Drawing
  $bitmap = New-Object System.Drawing.Bitmap 256, 256
  $graphics = [System.Drawing.Graphics]::FromImage($bitmap)
  $graphics.SmoothingMode = [System.Drawing.Drawing2D.SmoothingMode]::AntiAlias
  $graphics.Clear([System.Drawing.Color]::FromArgb(16, 36, 31))
  $green = New-Object System.Drawing.SolidBrush ([System.Drawing.Color]::FromArgb(93, 214, 137))
  $whitePen = New-Object System.Drawing.Pen ([System.Drawing.Color]::White, 10)
  $whitePen.Alignment = [System.Drawing.Drawing2D.PenAlignment]::Center
  $graphics.FillEllipse($green, 24, 24, 208, 208)
  $graphics.DrawArc($whitePen, 44, 36, 170, 184, 70, 220)
  $graphics.DrawArc($whitePen, 42, 36, 170, 184, 250, 220)
  $icon = [System.Drawing.Icon]::FromHandle($bitmap.GetHicon())
  $stream = [System.IO.File]::Open($iconPath, [System.IO.FileMode]::Create)
  $icon.Save($stream)
  $stream.Close()
  $graphics.Dispose()
  $bitmap.Dispose()
  $icon.Dispose()
}

& windres --target pe-x86-64 $rcPath -O coff -o $sysoPath

$env:GOCACHE = $cacheDir
go test ./...
go build -ldflags="-H=windowsgui" -o $exePath ./cmd/server

Get-Item $exePath | Select-Object FullName, Length, LastWriteTime
