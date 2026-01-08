# Runs k6 load test while periodically killing and restarting workers

param(
    [int]$ChurnIntervalSeconds = 20,    # how often to churn a worker
    [int]$TestDurationMinutes = 3,      # total test duration
    [string]$WorkerPrefix = "geostreamdb-worker"  # docker container name prefix
)

$ErrorActionPreference = "Stop"

# get all worker containers
function Get-Workers {
    docker ps --filter "name=$WorkerPrefix" --format "{{.Names}}" | Where-Object { $_ }
}

# kill a random worker
function Kill-RandomWorker {
    $workers = @(Get-Workers)
    if ($workers.Count -le 1) {
        Write-Host "[CHURN] Only $($workers.Count) worker(s) running, skipping kill to maintain availability"
        return $null
    }
    
    $victim = $workers | Get-Random
    Write-Host "[CHURN] Killing worker: $victim"
    docker stop $victim --time 1 | Out-Null
    return $victim
}

# restart a stopped worker
function Restart-Worker {
    param([string]$WorkerName)
    
    if ($WorkerName) {
        Write-Host "[CHURN] Restarting worker: $WorkerName"
        docker start $WorkerName | Out-Null
    }
}

# main orchestration
Write-Host "=========================================="
Write-Host "Worker Churn Test"
Write-Host "=========================================="
Write-Host "Churn interval: ${ChurnIntervalSeconds}s"
Write-Host "Test duration: ${TestDurationMinutes}m"
Write-Host ""

# check initial worker count
$initialWorkers = @(Get-Workers)
Write-Host "[INIT] Found $($initialWorkers.Count) workers: $($initialWorkers -join ', ')"

if ($initialWorkers.Count -lt 2) {
    Write-Host "[ERROR] Need at least 2 workers for churn test. Start more workers first."
    exit 1
}

# start k6 in background
Write-Host "[TEST] Starting k6 load test..."
$k6Dir = Join-Path $PSScriptRoot "k6"
$k6Job = Start-Job -ScriptBlock {
    param($duration, $dir)
    Set-Location $dir
    k6 run --env DURATION="${duration}m" worker_churn.js 2>&1
} -ArgumentList $TestDurationMinutes, $k6Dir

# wait a bit for k6 to start
Start-Sleep -Seconds 5

# churn loop
$endTime = (Get-Date).AddMinutes($TestDurationMinutes)
$churnCount = 0
$lastKilled = $null

while ((Get-Date) -lt $endTime) {
    $remaining = [math]::Round(($endTime - (Get-Date)).TotalSeconds)
    $currentWorkers = @(Get-Workers)
    Write-Host "[STATUS] Workers: $($currentWorkers.Count), Time remaining: ${remaining}s"
    
    # alternate between kill and restart
    if ($churnCount % 2 -eq 0) {
        $lastKilled = Kill-RandomWorker
    } else {
        Restart-Worker -WorkerName $lastKilled
        $lastKilled = $null
    }
    
    $churnCount++
    
    # wait for churn interval or until test ends
    $sleepTime = [math]::Min($ChurnIntervalSeconds, $remaining)
    if ($sleepTime -gt 0) {
        Start-Sleep -Seconds $sleepTime
    }
}

# ensure all workers are back up
Write-Host "[CLEANUP] Restarting any stopped workers..."
if ($lastKilled) {
    Restart-Worker -WorkerName $lastKilled
}

# wait for k6 to finish
Write-Host "[TEST] Waiting for k6 to complete..."
$k6Output = Receive-Job -Job $k6Job -Wait
Remove-Job -Job $k6Job

Write-Host ""
Write-Host "=========================================="
Write-Host "Test Complete"
Write-Host "=========================================="
Write-Host "Total churn events: $churnCount"

