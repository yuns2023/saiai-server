# Install the SAIAI client, optionally without initializing any client config.
# Existing Claude Code and Codex CLI initialization entry points remain available.
#
# Usage via irm:
#   irm https://api.saiai.top/saiai-cli/setup.ps1 | iex
#   Invoke-Saiai install
#   Invoke-Saiai "https://api.saiai.top" "<api_key>"
#   Invoke-Saiai init-codex "https://api.saiai.top" "<api_key>"

function Get-SaiaiAssetName {
    $arch = [string]$env:PROCESSOR_ARCHITECTURE
    $arch = $arch.Trim().ToUpperInvariant()
    if ($arch -eq "X86") {
        $wow64Arch = [string]$env:PROCESSOR_ARCHITEW6432
        if (-not [string]::IsNullOrWhiteSpace($wow64Arch)) {
            $arch = $wow64Arch.Trim().ToUpperInvariant()
        }
    }
    switch ($arch) {
        "X64"     { $archName = "x86_64" }
        "AMD64"   { $archName = "x86_64" }
        "ARM64"   { $archName = "aarch64" }
        "AARCH64" { $archName = "aarch64" }
        default { throw "Unsupported architecture: $arch" }
    }

    return "saiai-windows-$archName.exe"
}

function Get-SaiaiDownloadBase {
    if ($env:SAIAI_DOWNLOAD_BASE) {
        return $env:SAIAI_DOWNLOAD_BASE.TrimEnd('/')
    }
    return "https://api.saiai.top/saiai-cli"
}

function Get-SaiaiDownloadUrl {
    $assetName = Get-SaiaiAssetName
    $downloadBase = Get-SaiaiDownloadBase
    return "$downloadBase/$assetName"
}

function Get-SaiaiManifestSha256 {
    param(
        [string]$ManifestPath,
        [string]$AssetName
    )
    $manifest = Get-Content -LiteralPath $ManifestPath -Raw | ConvertFrom-Json
    if ($null -eq $manifest.assets) {
        throw "Manifest does not include assets."
    }
    $assetProperty = $manifest.assets.PSObject.Properties[$AssetName]
    if ($null -eq $assetProperty -or $null -eq $assetProperty.Value.sha256) {
        throw "Manifest does not include sha256 for $AssetName."
    }
    $sha256 = [string]$assetProperty.Value.sha256
    if ($sha256.Length -ne 64) {
        throw "Manifest sha256 for $AssetName is invalid: $sha256"
    }
    return $sha256.ToLowerInvariant()
}

function Get-SaiaiFileSha256 {
    param([string]$Path)
    if (Get-Command Get-FileHash -ErrorAction SilentlyContinue) {
        return ((Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash).ToLowerInvariant()
    }

    $sha256 = [System.Security.Cryptography.SHA256]::Create()
    $stream = [System.IO.File]::OpenRead($Path)
    try {
        $hash = $sha256.ComputeHash($stream)
        return ([BitConverter]::ToString($hash)).Replace("-", "").ToLowerInvariant()
    }
    finally {
        $stream.Dispose()
        $sha256.Dispose()
    }
}

function Get-SaiaiInstallPath {
    if ($env:SAIAI_INSTALL_DIR) {
        $installDir = $env:SAIAI_INSTALL_DIR
    } elseif ($env:LOCALAPPDATA) {
        $installDir = Join-Path $env:LOCALAPPDATA "SAIAI\bin"
    } elseif ($env:USERPROFILE) {
        # Reconstruct the standard per-user application location without ever
        # falling back to the legacy ~/.saiai tree.
        $installDir = Join-Path $env:USERPROFILE "AppData\Local\SAIAI\bin"
    } else {
        throw "Cannot resolve install directory; set SAIAI_INSTALL_DIR."
    }
    return (Join-Path $installDir "saiai.exe")
}

function Add-SaiaiUserPath {
    param([string]$InstallDir)
    $currentUserPath = [Environment]::GetEnvironmentVariable("Path", "User")
    if ($null -eq $currentUserPath) {
        $currentUserPath = ""
    }
    $parts = $currentUserPath.Split(';') | Where-Object { -not [string]::IsNullOrWhiteSpace($_) }
    foreach ($part in $parts) {
        if ($part.TrimEnd('\') -ieq $InstallDir.TrimEnd('\')) {
            return
        }
    }
    $newPath = if ([string]::IsNullOrWhiteSpace($currentUserPath)) { $InstallDir } else { "$currentUserPath;$InstallDir" }
    [Environment]::SetEnvironmentVariable("Path", $newPath, "User")
    $env:Path = "$env:Path;$InstallDir"
}

function Install-SaiaiBinary {
    param(
        [string]$CandidatePath,
        [string]$InstallPath,
        [string]$InstallDir,
        [string]$ExpectedSha256
    )

    $null = New-Item -ItemType Directory -Path $InstallDir -Force
    if (Test-Path -LiteralPath $InstallPath -PathType Leaf) {
        try {
            $installedSha256 = Get-SaiaiFileSha256 -Path $InstallPath
            if ($installedSha256 -eq $ExpectedSha256) {
                Write-Host "SAIAI binary already current at $InstallPath" -ForegroundColor DarkGray
                return $InstallPath
            }
        }
        catch {
            Write-Warning "Could not verify existing SAIAI binary at $InstallPath; attempting replacement."
        }
    }

    try {
        if (Test-Path -LiteralPath $InstallPath -PathType Container) {
            throw "Install path is a directory: $InstallPath"
        }
        Copy-Item -LiteralPath $CandidatePath -Destination $InstallPath -Force -ErrorAction Stop
        Remove-Item -LiteralPath $CandidatePath -Force -ErrorAction SilentlyContinue
        return $InstallPath
    }
    catch {
        if (Test-Path -LiteralPath $InstallPath -PathType Leaf) {
            $message = "Could not replace existing SAIAI binary at $InstallPath. It may be running from saiai start. Continuing initialization with the downloaded binary; run saiai stop, rerun setup, then saiai start to update the installed copy."
            Write-Warning $message
            return $CandidatePath
        }
        throw
    }
}

function Invoke-Saiai {
    param(
        [Parameter(ValueFromRemainingArguments = $true)]
        [string[]]$Arguments
    )
    $installOnly = $Arguments.Count -eq 1 -and $Arguments[0] -eq "install"
    if ($Arguments.Count -gt 0 -and $Arguments[0] -eq "install" -and -not $installOnly) {
        Write-Error "The install mode accepts no additional arguments. Run 'saiai setup' after installation."
        return 1
    }
    if (-not $installOnly -and $Arguments.Count -lt 2) {
        Write-Error "Usage: Invoke-Saiai install OR Invoke-Saiai <base_url> <api_key> OR Invoke-Saiai init-codex <base_url> <api_key> [--websockets]"
        return 1
    }

    $assetName = Get-SaiaiAssetName
    $downloadBase = Get-SaiaiDownloadBase
    $downloadUrl = "$downloadBase/$assetName"
    $manifestUrl = "$downloadBase/manifest.json"
    $installPath = Get-SaiaiInstallPath
    $installDir = Split-Path -Parent $installPath
    $tmpDir = Join-Path ([System.IO.Path]::GetTempPath()) ("saiai-" + [guid]::NewGuid().ToString("N"))
    $null = New-Item -ItemType Directory -Path $tmpDir -Force
    $tmpBin = Join-Path $tmpDir "saiai.exe"
    $tmpManifest = Join-Path $tmpDir "manifest.json"

    try {
        $ProgressPreference = 'SilentlyContinue'
        [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
        $webClient = New-Object Net.WebClient
        Write-Host "Checking $manifestUrl" -ForegroundColor Cyan
        $webClient.DownloadFile($manifestUrl, $tmpManifest)
        $expectedSha256 = Get-SaiaiManifestSha256 -ManifestPath $tmpManifest -AssetName $assetName

        Write-Host "Downloading $downloadUrl" -ForegroundColor Cyan
        $webClient.DownloadFile($downloadUrl, $tmpBin)
        $actualSha256 = Get-SaiaiFileSha256 -Path $tmpBin
        if ($actualSha256 -ne $expectedSha256) {
            throw "SHA-256 mismatch for $assetName. Expected $expectedSha256, got $actualSha256."
        }
        $runPath = Install-SaiaiBinary -CandidatePath $tmpBin -InstallPath $installPath -InstallDir $installDir -ExpectedSha256 $expectedSha256
        if ($installOnly -and $runPath -ne $installPath) {
            throw "Install-only mode could not replace $installPath. Stop the running SAIAI process and try again."
        }
        Add-SaiaiUserPath -InstallDir $installDir

        if ($env:USERPROFILE) {
            $env:HOME = $env:USERPROFILE
        }
        $isCodexInit = -not $installOnly -and $Arguments[0] -eq "init-codex"
        if ($installOnly) {
            $exitCode = 0
        } else {
            if ($isCodexInit) {
                $forward = @()
                if ($Arguments.Count -gt 1) {
                    $forward = $Arguments[1..($Arguments.Count - 1)]
                }
                & $runPath init-codex @forward
            } else {
                & $runPath init @Arguments
            }
            $exitCode = $LASTEXITCODE
        }
        if ($exitCode -eq 0) {
            Write-Host "SAIAI available at $installPath" -ForegroundColor Green
            if ($installOnly) {
                Write-Host "Next, run: saiai setup" -ForegroundColor Green
            } elseif (-not $isCodexInit) {
                Write-Host "For Claude Code, start the local proxy with: saiai start" -ForegroundColor Green
            }
        } else {
            Write-Error "SAIAI failed with exit code $exitCode."
        }
        return $exitCode
    }
    finally {
        Remove-Item -LiteralPath $tmpDir -Force -Recurse -ErrorAction SilentlyContinue
    }
}

if ($args.Count -ge 1) {
    $code = Invoke-Saiai @args
    if ($code -ne 0) {
        exit $code
    }
}
