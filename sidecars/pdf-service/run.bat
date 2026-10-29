@echo off
rem pdf-service v0 启动脚本（Windows）
setlocal
cd /d "%~dp0"

rem 优先使用本服务 .venv，其次 bundled python，最后 PATH 中的 python
set "PY=%~dp0.venv\Scripts\python.exe"
if not exist "%PY%" set "PY=C:\Users\71408\.dsh\dsh-runtimes\dsh-primary-runtime\dependencies\python\python.exe"
if not exist "%PY%" set "PY=python"

if "%PDF_SERVICE_HOST%"=="" set "PDF_SERVICE_HOST=127.0.0.1"
if "%PDF_SERVICE_PORT%"=="" set "PDF_SERVICE_PORT=8000"

echo [pdf-service] python = %PY%
echo [pdf-service] listen = http://%PDF_SERVICE_HOST%:%PDF_SERVICE_PORT%
echo [pdf-service] health = curl http://%PDF_SERVICE_HOST%:%PDF_SERVICE_PORT%/healthz

"%PY%" -m uvicorn server:app --host %PDF_SERVICE_HOST% --port %PDF_SERVICE_PORT% --log-level info
endlocal
