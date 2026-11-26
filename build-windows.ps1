# Builds the NetKey for Windows installer (run on Windows, from this folder):
#   powershell -ExecutionPolicy Bypass -File build-windows.ps1
# Needs Go 1.26+, the .NET 10 SDK and Inno Setup 6 (winget install GoLang.Go Microsoft.DotNet.SDK.10 JRSoftware.InnoSetup).
# Output in stage\out: NetKey-<version>-setup.exe, plus the TAP-Windows6 source archive to attach to the release (GPL-2).
$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"
$root = $PSScriptRoot
$stage = Join-Path $root "stage"

# TAP-Windows6, OpenVPN's signed driver, unmodified. The release zip is pinned by its SHA-256.
$tapVersion = "9.27.0"
$tapZipUrl = "https://github.com/OpenVPN/tap-windows6/releases/download/$tapVersion/dist.win10.zip"
$tapZipSha256 = "36E2609B7CEEFEDCB978CE5C48CAF9E0E5AF83423717C4E2E3C1D7EBCA8F62A5"
$tapSourceUrl = "https://github.com/OpenVPN/tap-windows6/archive/refs/tags/$tapVersion.zip"
# Microsoft runtime packages whose license terms and notices ship with the app (see installer\THIRD-PARTY.txt)
$windowsAppSdk = "2.7.0"
$webView2 = "1.0.3719.77"

Remove-Item -Recurse -Force $stage -EA SilentlyContinue
New-Item -ItemType Directory -Force "$stage\driver", "$stage\licenses", "$stage\out" | Out-Null

Write-Host "TAP-Windows6 $tapVersion..."
$drvSrc = Join-Path $root "driver-src"
$tapZip = Join-Path $drvSrc "dist.win10.zip"
New-Item -ItemType Directory -Force $drvSrc | Out-Null
if (-not (Test-Path $tapZip)) { Invoke-WebRequest -UseBasicParsing $tapZipUrl -OutFile $tapZip }
$hash = (Get-FileHash $tapZip -Algorithm SHA256).Hash
if ($hash -ne $tapZipSha256) {
    Remove-Item $tapZip
    throw "dist.win10.zip has SHA-256 $hash, expected $tapZipSha256; deleted it. Check the TAP-Windows6 release."
}
Expand-Archive -Force $tapZip "$drvSrc\dist"
$drv = Get-ChildItem -Recurse "$drvSrc\dist" -Filter OemVista.inf | Where-Object { $_.Directory.Name -eq "amd64" } | Select-Object -First 1
if (-not $drv) { throw "no amd64 driver in dist.win10.zip" }
Copy-Item "$($drv.Directory.FullName)\*" "$stage\driver\"
Invoke-WebRequest -UseBasicParsing $tapSourceUrl -OutFile "$stage\out\tap-windows6-$tapVersion-source.zip"

Write-Host "service..."
Push-Location "$root\service"
$env:GOOS = "windows"; $env:GOARCH = "amd64"
go build -trimpath -ldflags "-s -w" -o "$stage\netkeyd.exe" ./cmd/netkeyd
if ($LASTEXITCODE) { throw "go build failed" }
Remove-Item Env:GOOS, Env:GOARCH
Pop-Location

Write-Host "app..."
dotnet publish "$root\app\NetKey.csproj" -c Release -r win-x64 -o "$stage\app" --nologo -v quiet
if ($LASTEXITCODE) { throw "dotnet publish failed" }

Write-Host "licenses..."
Copy-Item "$root\LICENSE", "$root\NOTICE", "$root\installer\THIRD-PARTY.txt" $stage
$nuget = if ($env:NUGET_PACKAGES) { $env:NUGET_PACKAGES } else { Join-Path $env:USERPROFILE ".nuget\packages" }
$dotnetRoot = Split-Path (Get-Command dotnet).Source
$licenses = @{
    "WindowsAppSDK-LICENSE.txt" = "$nuget\microsoft.windowsappsdk\$windowsAppSdk\license.txt"
    "WindowsAppSDK-NOTICE.txt"  = "$nuget\microsoft.windowsappsdk\$windowsAppSdk\NOTICE.txt"
    "WebView2-LICENSE.txt"      = "$nuget\microsoft.web.webview2\$webView2\LICENSE.txt"
    "WebView2-NOTICE.txt"       = "$nuget\microsoft.web.webview2\$webView2\NOTICE.txt"
    "DotNet-LICENSE.txt"        = "$dotnetRoot\LICENSE.txt"
    "DotNet-ThirdPartyNotices.txt" = "$dotnetRoot\ThirdPartyNotices.txt"
}
foreach ($name in $licenses.Keys) {
    if (-not (Test-Path $licenses[$name])) { throw "license file missing: $($licenses[$name])" }
    Copy-Item $licenses[$name] "$stage\licenses\$name"
}

Write-Host "installer..."
$iscc = @("${env:ProgramFiles(x86)}\Inno Setup 6\ISCC.exe", "$env:LOCALAPPDATA\Programs\Inno Setup 6\ISCC.exe") |
    Where-Object { Test-Path $_ } | Select-Object -First 1
if (-not $iscc) { throw "Inno Setup 6 not found" }
& $iscc /Q "/DStage=$stage" "$root\installer\NetKey.iss"
if ($LASTEXITCODE) { throw "ISCC failed" }
Get-ChildItem "$stage\out" | ForEach-Object { "$($_.FullName)  $([math]::Round($_.Length / 1MB, 1)) MB" }
