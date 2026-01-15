# k6 Test Runner

param(
    [ValidateSet(
        # direct k6 tests
        'aggregation', 'boundary', 'constraints', 'explosion', 'hotspot',
        'mixed-workload', 'spike', 'sustained-load', 'ttl', 'union',
        # orchestrated tests (require PowerShell scripts)
        'gateway-worker-latency', 'registry-disruption', 'registry-latency',
        'split-brain', 'worker-churn',
        # run all
        'all', 'all-direct', 'all-orchestrated'
    )]
    [string]$Test = 'sustained-load',
    [int]$Workers = 3,
    [int]$Gateways = 3,
    [string]$EntrypointUrl = 'http://localhost:8080',
    [string]$OutputDir = '.\k6\outputs',
    [switch]$SkipInfra
)

$ErrorActionPreference = "Stop"
$env:ENTRYPOINT_URL = $EntrypointUrl # load balancer is the entrypoint

# resolve output directory path
if ([string]::IsNullOrWhiteSpace($OutputDir)) {
    $OutputDir = Join-Path $PSScriptRoot "k6\outputs"
} elseif (-not [System.IO.Path]::IsPathRooted($OutputDir)) {
    $OutputDir = Join-Path $PSScriptRoot $OutputDir
}

# ensure output directory exists
if (-not (Test-Path $OutputDir)) {
    New-Item -ItemType Directory -Path $OutputDir -Force | Out-Null
}

# convert to absolute path for environment variable
$OutputDir = (Resolve-Path $OutputDir).Path
$env:K6_OUTPUT_DIR = $OutputDir

Write-Host "=== GeoStreamDB k6 Tests ===" -ForegroundColor Cyan
Write-Host "Test: $Test"
Write-Host "Workers: $Workers"
Write-Host "Gateways: $Gateways"
Write-Host "Entrypoint: $EntrypointUrl"
Write-Host "Output Directory: $OutputDir"
Write-Host ""

# check k6 is installed
try {
    k6 version | Out-Null
} catch {
    Write-Host "k6 not found. Install from: https://k6.io/docs/get-started/installation/" -ForegroundColor Red
    exit 1
}

# start infrastructure
if (-not $SkipInfra) {
    Write-Host "Starting infrastructure with $Workers workers and $Gateways gateways..." -ForegroundColor Yellow
    docker compose up --build --scale worker-node=$Workers --scale gateway=$Gateways -d
    
    Write-Host "Waiting for services..." -ForegroundColor Yellow
    for ($i = 0; $i -lt 30; $i++) {
        try {
            $response = Invoke-WebRequest -Uri "$EntrypointUrl/ping/0/0" -Method GET -TimeoutSec 2 -ErrorAction SilentlyContinue
            if ($response.StatusCode -ne 503) {
                Write-Host "Ready!" -ForegroundColor Green
                break
            }
        } catch {}
        Start-Sleep -Seconds 1
        Write-Host "." -NoNewline
    }
    Write-Host ""
}

# run tests
$testDir = Join-Path $PSScriptRoot "k6"
$startTime = Get-Date

function Run-K6Test {
    param([string]$TestFile, [string]$TestName)
    
    Write-Host ""
    Write-Host "--- Running $TestName ---" -ForegroundColor Cyan
    Push-Location $testDir
    try {
        k6 run --env ENTRYPOINT_URL=$EntrypointUrl $TestFile
    } finally {
        Pop-Location
    }
}

function Run-OrchestratedTest {
    param([string]$ScriptName, [string]$TestName)
    
    Write-Host ""
    Write-Host "--- Running $TestName (orchestrated) ---" -ForegroundColor Magenta
    Push-Location $PSScriptRoot
    try {
    $scriptPath = Join-Path $PSScriptRoot $ScriptName
        & $scriptPath
    } finally {
        Pop-Location
    }
}

# direct k6 tests (no orchestration needed)
$directTests = @{
    'aggregation'     = @{ file = 'aggregation.js';     name = 'Aggregation Test' }
    'boundary'        = @{ file = 'boundary.js';        name = 'Boundary Test' }
    'constraints'     = @{ file = 'constraints.js';     name = 'Constraints Test' }
    'explosion'       = @{ file = 'explosion.js';       name = 'Explosion Test' }
    'hotspot'         = @{ file = 'hotspot.js';         name = 'Hotspot Test' }
    'mixed-workload'  = @{ file = 'mixed_workload.js';  name = 'Mixed Workload Test' }
    'spike'           = @{ file = 'spike.js';           name = 'Spike Test' }
    'sustained-load'  = @{ file = 'sustained_load.js';  name = 'Sustained Load Test' }
    'ttl'             = @{ file = 'ttl.js';             name = 'TTL Expiration Test' }
    'union'           = @{ file = 'union.js';           name = 'Union Test' }
}

# orchestrated tests (require PowerShell scripts for chaos injection)
$orchestratedTests = @{
    'gateway-worker-latency' = @{ script = 'gateway-worker-latency.ps1'; name = 'Gateway-Worker Latency Test' }
    'registry-disruption'    = @{ script = 'registry-disruption.ps1';    name = 'Registry Disruption Test' }
    'registry-latency'       = @{ script = 'registry-latency.ps1';       name = 'Registry Latency Test' }
    'split-brain'            = @{ script = 'split-brain.ps1';            name = 'Split Brain Test' }
    'worker-churn'           = @{ script = 'worker-churn.ps1';           name = 'Worker Churn Test' }
}

switch ($Test) {
    # direct tests
    { $directTests.ContainsKey($_) } {
        $t = $directTests[$_]
        Run-K6Test $t.file $t.name
    }
    # orchestrated tests
    { $orchestratedTests.ContainsKey($_) } {
        $t = $orchestratedTests[$_]
        Run-OrchestratedTest $t.script $t.name
    }
    # run all direct tests
    'all-direct' {
        $keys = $directTests.Keys | Sort-Object
        for ($i = 0; $i -lt $keys.Count; $i++) {
            $t = $directTests[$keys[$i]]
            Run-K6Test $t.file $t.name
            if ($i -lt $keys.Count - 1) {
                Write-Host "Waiting for TTL expiration and system recovery..." -ForegroundColor Gray
                Start-Sleep -Seconds 30
            }
        }
    }
    # run all orchestrated tests
    'all-orchestrated' {
        $keys = $orchestratedTests.Keys | Sort-Object
        for ($i = 0; $i -lt $keys.Count; $i++) {
            $t = $orchestratedTests[$keys[$i]]
            Run-OrchestratedTest $t.script $t.name
            if ($i -lt $keys.Count - 1) {
                Write-Host "Restarting infrastructure for clean state..." -ForegroundColor Yellow
                docker compose restart
                Write-Host "Waiting for services to recover..." -ForegroundColor Gray
                Start-Sleep -Seconds 45
            }
        }
    }
    # run everything
    'all' {
        Write-Host "Running all direct tests first..." -ForegroundColor Yellow
        $keys = $directTests.Keys | Sort-Object
        for ($i = 0; $i -lt $keys.Count; $i++) {
            $t = $directTests[$keys[$i]]
            Run-K6Test $t.file $t.name
            Write-Host "Waiting for TTL expiration and system recovery..." -ForegroundColor Gray
            Start-Sleep -Seconds 30
        }
        Write-Host ""
        Write-Host "Running orchestrated tests..." -ForegroundColor Yellow
        $keys = $orchestratedTests.Keys | Sort-Object
        for ($i = 0; $i -lt $keys.Count; $i++) {
            $t = $orchestratedTests[$keys[$i]]
            Run-OrchestratedTest $t.script $t.name
            if ($i -lt $keys.Count - 1) {
                Write-Host "Restarting infrastructure for clean state..." -ForegroundColor Yellow
                docker compose restart
                Write-Host "Waiting for services to recover..." -ForegroundColor Gray
                Start-Sleep -Seconds 45
            }
        }
    }
}

$elapsed = (Get-Date) - $startTime
Write-Host ""
Write-Host "=== Tests Complete ===" -ForegroundColor Green
Write-Host ("Total time: {0:hh\:mm\:ss}" -f $elapsed) -ForegroundColor Cyan

if (-not $SkipInfra -and -not $env:CI) {
    $cleanup = Read-Host "Stop containers? (y/N)"
    if ($cleanup -eq "y") {
        docker compose down
    }
}