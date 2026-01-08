# Runs k6 load test while periodically stopping and restarting the registry

param(
    [int]$DisruptionIntervalSeconds = 30,   # how often to disrupt registry
    [int]$DowntimeSeconds = 15,             # how long registry stays down
    [int]$TestDurationMinutes = 3,          # total test duration
    [string]$RegistryContainer = "registry"  # registry container name
)

$ErrorActionPreference = "Stop"

# check if registry is running
function Test-Registry {
    try {
        $running = docker ps --filter "name=$RegistryContainer" --format "{{.Names}}" 2>$null
        return ($running -ne $null -and $running -ne "")
    } catch {
        return $false
    }
}

# stop registry
function Stop-Registry {
    Write-Host "[DISRUPTION] Stopping registry: $RegistryContainer"
    docker stop $RegistryContainer --time 1 | Out-Null
}

# start registry
function Start-Registry {
    Write-Host "[DISRUPTION] Starting registry: $RegistryContainer"
    docker start $RegistryContainer | Out-Null
}

# main orchestration
Write-Host "=========================================="
Write-Host "Registry Disruption Test"
Write-Host "=========================================="
Write-Host "Disruption interval: ${DisruptionIntervalSeconds}s"
Write-Host "Downtime per disruption: ${DowntimeSeconds}s"
Write-Host "Test duration: ${TestDurationMinutes}m"
Write-Host ""

# check registry exists and is running
if (-not (Test-Registry)) {
    Write-Host "[ERROR] Registry container '$RegistryContainer' not running."
    Write-Host "[ERROR] Make sure the system is up: docker-compose up -d"
    exit 1
}

Write-Host "[INIT] Registry is running: $RegistryContainer"

# start k6 in background
Write-Host "[TEST] Starting k6 load test..."
$k6Dir = Join-Path $PSScriptRoot "k6"
$k6Job = Start-Job -ScriptBlock {
    param($duration, $dir)
    Set-Location $dir
    k6 run --env DURATION="${duration}m" registry_disruption.js 2>&1
} -ArgumentList $TestDurationMinutes, $k6Dir

# wait for k6 to start
Start-Sleep -Seconds 5

# disruption loop
$endTime = (Get-Date).AddMinutes($TestDurationMinutes)
$disruptionCount = 0

while ((Get-Date) -lt $endTime) {
    $remaining = [math]::Round(($endTime - (Get-Date)).TotalSeconds)
    $isRunning = Test-Registry
    Write-Host "[STATUS] Registry: $(if ($isRunning) { 'UP' } else { 'DOWN' }), Time remaining: ${remaining}s"
    
    # wait until next disruption
    $waitTime = [math]::Min($DisruptionIntervalSeconds, $remaining)
    if ($waitTime -le 0) { break }
    
    Write-Host "[WAIT] Next disruption in ${waitTime}s..."
    Start-Sleep -Seconds $waitTime
    
    # check if we still have time for a full disruption cycle
    $remaining = [math]::Round(($endTime - (Get-Date)).TotalSeconds)
    if ($remaining -lt ($DowntimeSeconds + 5)) {
        Write-Host "[SKIP] Not enough time for disruption cycle, skipping"
        break
    }
    
    # stop registry
    Stop-Registry
    $disruptionCount++
    
    # wait while registry is down
    Write-Host "[DISRUPTION] Registry down for ${DowntimeSeconds}s..."
    Start-Sleep -Seconds $DowntimeSeconds
    
    # start registry
    Start-Registry
    
    # wait for registry to recover
    Write-Host "[RECOVERY] Waiting for registry to stabilize..."
    Start-Sleep -Seconds 5
    
    # verify recovery
    if (Test-Registry) {
        Write-Host "[RECOVERY] Registry is back up"
    } else {
        Write-Host "[ERROR] Registry failed to restart!"
        Start-Registry  # try again
    }
}

# ensure registry is up at the end
if (-not (Test-Registry)) {
    Write-Host "[CLEANUP] Restarting registry..."
    Start-Registry
    Start-Sleep -Seconds 3
}

# wait for k6 to finish
Write-Host "[TEST] Waiting for k6 to complete..."
$k6Output = Receive-Job -Job $k6Job -Wait
Remove-Job -Job $k6Job

Write-Host ""
Write-Host "=========================================="
Write-Host "Test Complete"
Write-Host "=========================================="
Write-Host "Total disruptions: $disruptionCount"
Write-Host "Registry status: $(if (Test-Registry) { 'UP' } else { 'DOWN' })"
Write-Host ""
Write-Host "Results saved to: registry_disruption_summary.json"

