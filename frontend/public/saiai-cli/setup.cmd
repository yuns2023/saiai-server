@echo off
setlocal EnableDelayedExpansion

rem V2 Preview (binary only; then run saiai setup): setup.cmd install
rem Existing Claude Code and Codex CLI initialization forms remain available.
set "INSTALLONLY=0"
if /I "%~1"=="install" (
  if not "%~2"=="" goto install_usage
  set "INSTALLONLY=1"
) else (
  if "%~2"=="" goto usage
)

set "ARCH=%PROCESSOR_ARCHITECTURE%"
if /I "%ARCH%"=="X86" if /I not "%PROCESSOR_ARCHITEW6432%"=="" set "ARCH=%PROCESSOR_ARCHITEW6432%"
if /I "%ARCH%"=="AMD64" set "ASSET=saiai-windows-x86_64.exe"
if /I "%ARCH%"=="ARM64" set "ASSET=saiai-windows-aarch64.exe"
if not defined ASSET (
  echo Unsupported architecture: %ARCH%
  exit /b 1
)

if not "%SAIAI_DOWNLOAD_BASE%"=="" (
  set "DOWNLOAD_BASE=%SAIAI_DOWNLOAD_BASE%"
) else (
  set "DOWNLOAD_BASE=https://api.saiai.top/saiai-cli"
)
if "%DOWNLOAD_BASE:~-1%"=="/" set "DOWNLOAD_BASE=%DOWNLOAD_BASE:~0,-1%"
set "URL=%DOWNLOAD_BASE%/%ASSET%"
set "MANIFESTURL=%DOWNLOAD_BASE%/manifest.json"

if not "%SAIAI_INSTALL_DIR%"=="" (
  set "INSTALLDIR=%SAIAI_INSTALL_DIR%"
) else if not "%LOCALAPPDATA%"=="" (
  set "INSTALLDIR=%LOCALAPPDATA%\SAIAI\bin"
) else (
  rem Reconstruct the standard per-user application location; never use legacy .saiai.
  set "INSTALLDIR=%USERPROFILE%\AppData\Local\SAIAI\bin"
)

set "TMPDIR=%TEMP%\saiai-%RANDOM%%RANDOM%"
mkdir "%TMPDIR%" >nul 2>nul
mkdir "%INSTALLDIR%" >nul 2>nul
set "BIN=%TMPDIR%\saiai.exe"
set "MANIFEST=%TMPDIR%\manifest.json"
set "EXPECTEDFILE=%TMPDIR%\expected.sha256"
set "ACTUALFILE=%TMPDIR%\actual.sha256"
set "INSTALLBIN=%INSTALLDIR%\saiai.exe"

echo Checking %MANIFESTURL%
powershell -NoProfile -ExecutionPolicy Bypass -Command ^
  "$ProgressPreference='SilentlyContinue'; [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12; (New-Object Net.WebClient).DownloadFile('%MANIFESTURL%', '%MANIFEST%')"
if errorlevel 1 (
  echo Failed to download %MANIFESTURL%
  rd /s /q "%TMPDIR%" >nul 2>nul
  exit /b 1
)

powershell -NoProfile -ExecutionPolicy Bypass -Command ^
  "$manifest = Get-Content -LiteralPath '%MANIFEST%' -Raw | ConvertFrom-Json; $asset = $manifest.assets.PSObject.Properties['%ASSET%']; if ($null -eq $asset -or $null -eq $asset.Value.sha256) { throw 'Manifest does not include sha256 for %ASSET%.' }; $sha = [string]$asset.Value.sha256; if ($sha.Length -ne 64) { throw 'Manifest sha256 for %ASSET% is invalid.' }; [System.IO.File]::WriteAllText('%EXPECTEDFILE%', $sha.ToLowerInvariant())"
if errorlevel 1 (
  echo Failed to read sha256 for %ASSET% from %MANIFESTURL%
  rd /s /q "%TMPDIR%" >nul 2>nul
  exit /b 1
)
set /p EXPECTED_SHA256=<"%EXPECTEDFILE%"

echo Downloading %URL%
powershell -NoProfile -ExecutionPolicy Bypass -Command ^
  "$ProgressPreference='SilentlyContinue'; [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12; (New-Object Net.WebClient).DownloadFile('%URL%', '%BIN%')"
if errorlevel 1 (
  echo Failed to download %URL%
  rd /s /q "%TMPDIR%" >nul 2>nul
  exit /b 1
)

powershell -NoProfile -ExecutionPolicy Bypass -Command ^
  "$sha256 = [System.Security.Cryptography.SHA256]::Create(); $stream = [System.IO.File]::OpenRead('%BIN%'); try { $hash = $sha256.ComputeHash($stream); $hex = ([System.BitConverter]::ToString($hash)).Replace('-', '').ToLowerInvariant(); [System.IO.File]::WriteAllText('%ACTUALFILE%', $hex) } finally { $stream.Dispose(); $sha256.Dispose() }"
if errorlevel 1 (
  echo Failed to calculate SHA-256 for %BIN%
  rd /s /q "%TMPDIR%" >nul 2>nul
  exit /b 1
)
set /p ACTUAL_SHA256=<"%ACTUALFILE%"
if /I not "%ACTUAL_SHA256%"=="%EXPECTED_SHA256%" (
  echo SHA-256 mismatch for %ASSET%
  echo   expected: %EXPECTED_SHA256%
  echo   actual:   %ACTUAL_SHA256%
  rd /s /q "%TMPDIR%" >nul 2>nul
  exit /b 1
)

move /Y "%BIN%" "%INSTALLBIN%" >nul
if errorlevel 1 (
  echo Failed to install SAIAI at %INSTALLBIN%
  rd /s /q "%TMPDIR%" >nul 2>nul
  exit /b 1
)
if not "%USERPROFILE%"=="" set "HOME=%USERPROFILE%"

if "%INSTALLONLY%"=="1" (
  set "EXITCODE=0"
) else (
  if /I "%~1"=="init-codex" (
    "%INSTALLBIN%" %*
  ) else (
    "%INSTALLBIN%" init %*
  )
  set "EXITCODE=!ERRORLEVEL!"
)
if "%EXITCODE%"=="0" (
  echo SAIAI installed at %INSTALLBIN%
  if "%INSTALLONLY%"=="1" (
    echo Next, run: saiai setup
  ) else (
    echo For Claude Code, start the local proxy with: saiai start
  )
)
rd /s /q "%TMPDIR%" >nul 2>nul
exit /b %EXITCODE%

:usage
echo Usage:
echo   setup.cmd install
echo   setup.cmd ^<base_url^> ^<api_key^>
echo   setup.cmd init-codex ^<base_url^> ^<api_key^> [--websockets]
exit /b 1

:install_usage
echo The install mode accepts no additional arguments. Run "saiai setup" after installation.
goto usage
