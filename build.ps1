param([string]$Go = 'go', [string]$Iscc = '', [switch]$BinaryOnly)
$ErrorActionPreference = 'Stop'
Push-Location $PSScriptRoot
try {
    & $Go test ./...
    if ($LASTEXITCODE) { throw 'Не прошли тесты' }
    New-Item -ItemType Directory -Force dist | Out-Null
    & $Go build -trimpath -ldflags '-s -w -H=windowsgui' -o dist/engmcp.exe .
    if ($LASTEXITCODE) { throw 'Ошибка сборки EngMCP' }
    if (!$BinaryOnly) {
        & python -X utf8 tests/engineering_services.py
        if ($LASTEXITCODE) { throw 'Не прошла проверка GitLab, Jenkins и Allure через MCP' }
        if (!$Iscc) {
            $Iscc = @("$env:LOCALAPPDATA/Programs/Inno Setup 6/ISCC.exe", "${env:ProgramFiles(x86)}/Inno Setup 6/ISCC.exe") | Where-Object { Test-Path -LiteralPath $_ } | Select-Object -First 1
        }
        if (!$Iscc) { throw 'Укажи путь к ISCC.exe через -Iscc' }
        & $Iscc /Qp packaging/engmcp.iss
        if ($LASTEXITCODE) { throw 'Ошибка сборки установщика' }
    }
} finally { Pop-Location }
