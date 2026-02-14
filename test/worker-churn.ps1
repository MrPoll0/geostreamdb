# Runs k6 load test while periodically killing and restarting workers

param(
    [int]$ChurnIntervalSeconds = 20,    # how often to churn a worker
    [int]$TestDurationMinutes = 3,      # total test duration
    [string]$WorkerPrefix = "geostreamdb-worker",  # docker container name prefix
    [string]$Namespace = "geostreamdb",
    [switch]$UseKubernetes = $false # toggle between Docker Compose (default) and Kubernetes
)

$ErrorActionPreference = "Stop"

# get all worker containers/pods
function Get-Workers {
    if ($UseKubernetes) {
        $pods = kubectl get pods -n $Namespace -l app=worker-node --field-selector=status.phase=Running -o jsonpath='{.items[*].metadata.name}' 2>$null
        if ($pods) {
            return @($pods -split ' ' | Where-Object { $_ })
        }
        return @()
    } else {
        docker ps --filter "name=$WorkerPrefix" --format "{{.Names}}" | Where-Object { $_ }
    }
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
    if ($UseKubernetes) {
        kubectl delete pod $victim -n $Namespace --grace-period=1
        # Kubernetes will auto-restart, so return null (no need to track for restart)
        return $null
    } else {
        docker stop $victim --time 1 | Out-Null
        return $victim
    }
}

# restart a stopped worker (only needed for Docker Compose)
function Restart-Worker {
    param([string]$WorkerName)
    
    if ($WorkerName -and -not $UseKubernetes) {
        Write-Host "[CHURN] Restarting worker: $WorkerName"
        docker start $WorkerName | Out-Null
    }
    # Kubernetes auto-restarts pods, so no action needed
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
$entrypoint = $env:ENTRYPOINT_URL
if (-not $entrypoint) { $entrypoint = "http://localhost:8080" }
$k6Job = Start-Job -ScriptBlock {
    param($duration, $dir, $url)
    Set-Location $dir
    k6 run --env DURATION="${duration}m" --env ENTRYPOINT_URL=$url worker_churn.js 2>&1
} -ArgumentList $TestDurationMinutes, $k6Dir, $entrypoint

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