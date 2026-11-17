param([switch]$CheckOnly)

$ErrorActionPreference = 'Stop'
try {
    $magpieExe = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..\magpie.exe'))
    if (-not (Test-Path -LiteralPath $magpieExe)) {
        throw 'Build magpie.exe in the repository folder first (go build -o magpie.exe).'
    }
    if (-not $env:MAGPIE_ACCIO_BIN -and (Test-Path -LiteralPath 'D:\accio\Accio.exe')) {
        $env:MAGPIE_ACCIO_BIN = 'D:\accio\Accio.exe'
    }
    if ($env:MAGPIE_ACCIO_BIN -and -not (Test-Path -LiteralPath $env:MAGPIE_ACCIO_BIN)) {
        throw 'MAGPIE_ACCIO_BIN does not point to an existing Accio executable.'
    }
    $launchEnv = @(& $magpieExe accio env)
    if ($LASTEXITCODE -ne 0) { throw 'Could not read the Accio settings from Magpie.' }
    $gatewayLine = $launchEnv | Where-Object { $_ -like 'GATEWAY_BASE_URL=*' } | Select-Object -First 1
    if (-not $gatewayLine) {
        throw 'Choose a model in Magpie > Agents > Accio Work first.'
    }
    $gatewayBase = $gatewayLine.Substring('GATEWAY_BASE_URL='.Length)
    try {
        $catalog = Invoke-RestMethod -Uri ($gatewayBase + '/api/llm/config/v2') -Method Post -ContentType 'application/json' -Body '{"supportAutoModel":true}' -TimeoutSec 5
    } catch {
        throw 'Cannot reach the Magpie model list. Open magpie.exe and keep it running, then try again.'
    }
    $models = @($catalog | ForEach-Object { $_.modelList } | Where-Object { $_.modelCode })
    if ($models.Count -eq 0) { throw 'Magpie returned no models. Check the provider and model settings.' }
    Write-Host ('Magpie models: ' + $models.Count)
    $models | ForEach-Object { Write-Host ('  ' + $_.modelCode) }
    if ($CheckOnly) { exit 0 }
    if (Get-Process -Name Accio -ErrorAction SilentlyContinue) {
        throw 'Accio is already running. Fully quit Accio from its tray menu, then double-click Start-Accio-Magpie.cmd again.'
    }
    & $magpieExe accio run
    exit $LASTEXITCODE
} catch {
    Write-Host $_.Exception.Message -ForegroundColor Red
    exit 1
}
