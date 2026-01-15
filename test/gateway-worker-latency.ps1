# Runs k6 load test while injecting network latency on worker containers
# This affects the gateway<->worker data path (ping writes and reads)

param(
    [int]$LatencyMs = 500,                  # latency to inject (ms)
    [int]$JitterMs = 100,                   # jitter/variance (ms)
    [int]$LatencyIntervalSeconds = 30,      # how often to toggle latency
    [int]$LatencyDurationSeconds = 20,      # how long latency is active per cycle
    [int]$TestDurationMinutes = 3,          # total test duration
    [string]$WorkerPrefix = "geostreamdb-worker"  # worker container name prefix
)

$ErrorActionPreference = "Stop"

# get all worker containers
function Get-Workers {
    $workers = docker ps --filter "name=$WorkerPrefix" --format "{{.Names}}" 2>$null
    if ($workers) {
        return @($workers -split "`n" | Where-Object { $_ })
    }
    return @()
}

# get container PID for nsenter
function Get-ContainerPid {
    param([string]$Container)
    $cPid = docker inspect -f '{{.State.Pid}}' $Container 2>$null
    if ($LASTEXITCODE -ne 0 -or -not $cPid) {
        return $null
    }
    return $cPid
}

# add network latency to a container
function Add-NetworkDelay {
    param([string]$Container, [int]$LatencyMs, [int]$JitterMs)
    
    $cPid = Get-ContainerPid -Container $Container
    if (-not $cPid) {
        Write-Host "[ERROR] Could not get PID for container $Container"
        return $false
    }
    
    try {
        $ErrorActionPreference = "SilentlyContinue"
        # run tc in the container's network namespace using nsenter from alpine sidecar container to inject latency
        $result = docker run --rm --privileged --pid=host alpine sh -c `
            "apk add --no-cache -q iproute2 >/dev/null 2>&1; nsenter -t $cPid -n tc qdisc add dev eth0 root netem delay ${LatencyMs}ms ${JitterMs}ms" 2>&1
        $exitCode = $LASTEXITCODE
        $ErrorActionPreference = "Stop"
    } catch {
        return $false
    }
    
    return $exitCode -eq 0
}

# remove network latency from a container
function Remove-NetworkDelay {
    param([string]$Container)
    
    $cPid = Get-ContainerPid -Container $Container
    if (-not $cPid) {
        return $false
    }
    
    try {
        $ErrorActionPreference = "SilentlyContinue"
        # run tc in the container's network namespace using nsenter from alpine sidecar container to remove latency
        docker run --rm --privileged --pid=host alpine sh -c `
            "apk add --no-cache -q iproute2 >/dev/null 2>&1; nsenter -t $cPid -n tc qdisc del dev eth0 root 2>/dev/null; exit 0" 2>&1 | Out-Null
        $ErrorActionPreference = "Stop"
    } catch {
        # Ignore
    }
    
    return $true
}

# add latency to all workers
function Add-LatencyToAllWorkers {
    param([int]$LatencyMs, [int]$JitterMs)
    
    $workers = Get-Workers
    $successCount = 0
    
    foreach ($worker in $workers) {
        if (Add-NetworkDelay -Container $worker -LatencyMs $LatencyMs -JitterMs $JitterMs) {
            $successCount++
        }
    }
    
    Write-Host "[LATENCY] Added ${LatencyMs}ms +/- ${JitterMs}ms delay to $successCount/$($workers.Count) workers"
    return $successCount -gt 0
}

# remove latency from all workers
function Remove-LatencyFromAllWorkers {
    $workers = Get-Workers
    
    foreach ($worker in $workers) {
        Remove-NetworkDelay -Container $worker | Out-Null
    }
    
    Write-Host "[LATENCY] Removed delay from all workers"
}

# main orchestration
Write-Host "=========================================="
Write-Host "Gateway-Worker Latency Test (tc/netem)"
Write-Host "=========================================="
Write-Host "Latency: ${LatencyMs}ms +/- ${JitterMs}ms"
Write-Host "Latency interval: ${LatencyIntervalSeconds}s"
Write-Host "Latency duration: ${LatencyDurationSeconds}s per cycle"
Write-Host "Test duration: ${TestDurationMinutes}m"
Write-Host ""

# pre-pull alpine image
Write-Host "[INIT] Pulling alpine image..."
docker pull alpine 2>&1 | Out-Null

# check workers exist
Write-Host "[INIT] Checking worker containers..."
$workers = Get-Workers
if ($workers.Count -eq 0) {
    Write-Host "[ERROR] No worker containers found matching prefix '$WorkerPrefix'"
    Write-Host "[ERROR] Make sure the system is up: docker-compose up -d"
    exit 1
}
Write-Host "[INIT] Found $($workers.Count) workers: $($workers -join ', ')"

# clean up any existing latency
Write-Host "[INIT] Cleaning up any existing latency..."
Remove-LatencyFromAllWorkers

# start k6 in background
Write-Host "[TEST] Starting k6 load test..."
$k6Dir = Join-Path $PSScriptRoot "k6"
$entrypoint = $env:ENTRYPOINT_URL
if (-not $entrypoint) { $entrypoint = "http://localhost:8080" }
$k6Job = Start-Job -ScriptBlock {
    param($duration, $dir, $url)
    Set-Location $dir
    k6 run --env DURATION="${duration}m" --env ENTRYPOINT_URL=$url gateway_worker_latency.js 2>&1
} -ArgumentList $TestDurationMinutes, $k6Dir, $entrypoint

# wait for k6 to start
Start-Sleep -Seconds 5

# latency injection loop
$endTime = (Get-Date).AddMinutes($TestDurationMinutes)
$cycleCount = 0
$latencyActive = $false

while ((Get-Date) -lt $endTime) {
    $remaining = [math]::Round(($endTime - (Get-Date)).TotalSeconds)
    Write-Host "[STATUS] Latency: $(if ($latencyActive) { 'ACTIVE' } else { 'OFF' }), Time remaining: ${remaining}s"
    
    if (-not $latencyActive) {
        # wait before injecting latency
        $waitTime = [math]::Min($LatencyIntervalSeconds, $remaining)
        if ($waitTime -le 0) { break }
        
        Write-Host "[WAIT] Next latency injection in ${waitTime}s..."
        Start-Sleep -Seconds $waitTime
        
        # check if we have time for a latency cycle
        $remaining = [math]::Round(($endTime - (Get-Date)).TotalSeconds)
        if ($remaining -lt ($LatencyDurationSeconds + 5)) {
            Write-Host "[SKIP] Not enough time for latency cycle, skipping"
            break
        }
        
        # inject latency on all workers
        if (Add-LatencyToAllWorkers -LatencyMs $LatencyMs -JitterMs $JitterMs) {
            $latencyActive = $true
            $cycleCount++
        }
        
    } else {
        # latency is active, wait for duration then remove
        Write-Host "[LATENCY] Latency active for ${LatencyDurationSeconds}s..."
        Start-Sleep -Seconds $LatencyDurationSeconds
        
        Remove-LatencyFromAllWorkers
        $latencyActive = $false
        
        Write-Host "[RECOVERY] Latency removed, system recovering..."
        Start-Sleep -Seconds 5
    }
}

# cleanup: ensure latency is removed
if ($latencyActive) {
    Remove-LatencyFromAllWorkers
}

# wait for k6 to finish
Write-Host "[TEST] Waiting for k6 to complete..."
$k6Output = Receive-Job -Job $k6Job -Wait
Remove-Job -Job $k6Job

Write-Host ""
Write-Host "=========================================="
Write-Host "Test Complete"
Write-Host "=========================================="
Write-Host "Latency cycles: $cycleCount"
Write-Host "Workers affected: $($workers.Count)"
Write-Host "Latency per cycle: ${LatencyMs}ms +/- ${JitterMs}ms"
Write-Host ""
Write-Host "Results saved to: test/k6/outputs/gateway_worker_latency_summary.json"