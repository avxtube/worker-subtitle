@echo off
setlocal
pushd "%~dp0"
if errorlevel 1 exit /b 1

set "GOOS=windows"
set "GOARCH=amd64"
set "CGO_ENABLED=0"
set "BINARY=.build\windows.exe"
if /i "%~1"=="linux" (
    set "GOOS=linux"
    set "BINARY=.build\linux"
) else if not "%~1"=="" if /i not "%~1"=="windows" (
    echo Usage: build.bat [windows^|linux]
    goto :failed
)

echo Building Worker Subtitle for %GOOS%...
if not exist ".build" mkdir ".build"
if errorlevel 1 goto :failed
go build -trimpath -o "%BINARY%" ./cmd
if errorlevel 1 goto :failed
echo Build successful: %BINARY%

echo Copying MOSS runtime files...
robocopy "moss" ".build\moss" *.py requirements.txt default_prompt.txt LICENSE-MOSS NOTICE.md /S /XD __pycache__ /NFL /NDL /NJH /NJS /NP
if errorlevel 8 goto :failed

echo Copying configuration and documentation...
for %%F in (install.ps1 install.sh .env.example README.md) do (
    copy /Y "%%F" ".build\%%F" >nul
    if errorlevel 1 goto :failed
)
if exist ".env" (
    copy /Y ".env" ".build\.env" >nul
    if errorlevel 1 goto :failed
    echo Copied .env successfully.
) else (
    echo No source .env found; existing runtime configuration is kept.
)
if not exist ".build\models" mkdir ".build\models"
if errorlevel 1 goto :failed

echo Build and copy completed. Run %BINARY% to open the dashboard.
popd
exit /b 0

:failed
echo Build or copy failed.
popd
exit /b 1
