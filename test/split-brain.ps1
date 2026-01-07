# Tests split-brain condition by blocking heartbeats to one gateway
# This causes that gateway to lose all workers after TTL expires,
# creating inconsistent state across gateways

param(
    [int]$TestDurationMinutes = 2,
    [int]$WaitForTTLSeconds = 15,          # wait for workers to expire (TTL = 10s)
    [string]$GatewayPrefix = "geostreamdb-gateway"
)

$ErrorActionPreference = "Stop"

# get all gateway containers
function Get-Gateways {
    $gateways = docker ps --filter "name=$GatewayPrefix" --format "{{.Names}}" 2>$null
    if ($gateways) {
        return @($gateways -split "`n" | Where-Object { $_ })
    }
    return @()
}

# get container PID
function Get-ContainerPid {
    param([string]$Container)
    $cPid = docker inspect -f '{{.State.Pid}}' $Container 2>$null
    if ($LASTEXITCODE -ne 0 -or -not $cPid) {
        return $null
    }
    return $cPid
}

# block heartbeats from registry to a gateway (drop packets from registry's gRPC port)
function Block-Heartbeats {
    param([string]$Container)
    
    $cPid = Get-ContainerPid -Container $Container
    if (-not $cPid) {
        Write-Host "[ERROR] Could not get PID for container $Container"
        return $false
    }
    
    try {
        $ErrorActionPreference = "SilentlyContinue"
        # drop incoming TCP packets from source port 50051 (registry's gRPC)
        # this blocks heartbeat forwarding from registry to this gateway
        docker run --rm --privileged --pid=host alpine sh -c `
            "apk add --no-cache -q iptables >/dev/null 2>&1; nsenter -t $cPid -n iptables -A INPUT -p tcp --sport 50051 -j DROP" 2>&1 | Out-Null
        $ErrorActionPreference = "Stop"
        Write-Host "[BLOCK] Blocked heartbeats to $Container"
        return $true
    } catch {
        Write-Host "[ERROR] Failed to block heartbeats: $_"
        return $false
    }
}

# unblock heartbeats
function Unblock-Heartbeats {
    param([string]$Container)
    
    $cPid = Get-ContainerPid -Container $Container
    if (-not $cPid) {
        return $false
    }
    
    try {
        $ErrorActionPreference = "SilentlyContinue"
        # remove the iptables rule that blocks heartbeats
        docker run --rm --privileged --pid=host alpine sh -c `
            "apk add --no-cache -q iptables >/dev/null 2>&1; nsenter -t $cPid -n iptables -D INPUT -p tcp --sport 50051 -j DROP 2>/dev/null; exit 0" 2>&1 | Out-Null
        $ErrorActionPreference = "Stop"
        Write-Host "[UNBLOCK] Unblocked heartbeats to $Container"
        return $true
    } catch {
        return $true  # ignore errors
    }
}

# quick consistency check (before test)
function Test-Consistency {
    param([int]$GatewayCount)
    
    $counts = @()
    for ($i = 0; $i -lt $GatewayCount; $i++) {
        try {
            $response = Invoke-RestMethod -Uri "http://localhost:8080/metrics" -TimeoutSec 5
            $match = $response | Select-String -Pattern "gateway_worker_nodes_total (\d+)"
            if ($match) {
                $counts += [int]$match.Matches[0].Groups[1].Value
            }
        } catch {
            $counts += -1
        }
    }
    return $counts
}

# main orchestration
Write-Host "=========================================="
Write-Host "Split-Brain Detection Test"
Write-Host "=========================================="
Write-Host "Test duration: ${TestDurationMinutes}m"
Write-Host "Wait for TTL: ${WaitForTTLSeconds}s"
Write-Host ""

# pre-pull alpine image
Write-Host "[INIT] Pulling alpine image..."
docker pull alpine 2>&1 | Out-Null

# get gateways
Write-Host "[INIT] Finding gateway containers..."
$gateways = Get-Gateways
if ($gateways.Count -eq 0) {
    Write-Host "[ERROR] No gateway containers found"
    exit 1
}
Write-Host "[INIT] Found $($gateways.Count) gateways: $($gateways -join ', ')"

# select one gateway to make "blind"
$blindGateway = $gateways[0]
Write-Host "[INIT] Target gateway (will become blind): $blindGateway"

# check initial consistency
Write-Host ""
Write-Host "[CHECK] Initial consistency check..."
$initialCounts = Test-Consistency -GatewayCount $gateways.Count
Write-Host ("[CHECK] Worker counts: [" + ($initialCounts -join ', ') + "]")
if (($initialCounts | Sort-Object -Unique).Count -eq 1) {
    Write-Host "[CHECK] All gateways consistent"
} else {
    Write-Host "[WARN] Gateways already inconsistent!"
}

# block heartbeats to the target gateway
Write-Host ""
Write-Host "[BLOCK] Blocking heartbeats to $blindGateway..."
if (-not (Block-Heartbeats -Container $blindGateway)) {
    Write-Host "[ERROR] Failed to block heartbeats"
    exit 1
}

# wait for TTL to expire
Write-Host "[WAIT] Waiting ${WaitForTTLSeconds}s for worker TTL to expire..."
Start-Sleep -Seconds $WaitForTTLSeconds

# check for split-brain
Write-Host ""
Write-Host "[CHECK] Post-block consistency check..."
$postBlockCounts = Test-Consistency -GatewayCount $gateways.Count
Write-Host ("[CHECK] Worker counts: [" + ($postBlockCounts -join ', ') + "]")
$uniqueCounts = $postBlockCounts | Sort-Object -Unique
if ($uniqueCounts.Count -gt 1) {
    Write-Host "[DETECTED] SPLIT-BRAIN: Gateways have different worker counts!"
    $minWorkers = ($postBlockCounts | Where-Object { $_ -eq ($postBlockCounts | Measure-Object -Minimum).Minimum } | Select-Object -First 1)
    Write-Host "[DETECTED] Blind gateway reports: $minWorkers workers"
} else {
    Write-Host "[CHECK] All gateways still consistent (split-brain not triggered yet)"
}

# start k6 test
Write-Host ""
Write-Host "[TEST] Starting k6 split-brain detection test..."
$k6Job = Start-Job -ScriptBlock {
    param($duration, $gatewayCount)
    Set-Location $using:PWD
    k6 run --env DURATION="${duration}m" --env GATEWAY_COUNT=$gatewayCount test/k6/split_brain.js 2>&1
} -ArgumentList $TestDurationMinutes, $gateways.Count

# monitor during test
$endTime = (Get-Date).AddMinutes($TestDurationMinutes)
while ((Get-Date) -lt $endTime) {
    Start-Sleep -Seconds 10
    $remaining = [math]::Round(($endTime - (Get-Date)).TotalSeconds)
    $counts = Test-Consistency -GatewayCount $gateways.Count
    $isConsistent = (($counts | Sort-Object -Unique).Count -eq 1)
    $status = if ($isConsistent) { "Consistent" } else { "SPLIT-BRAIN!" }
    Write-Host ("[MONITOR] Worker counts: [" + ($counts -join ', ') + "] - $status, ${remaining}s remaining")
}

# wait for k6 to finish
Write-Host ""
Write-Host "[TEST] Waiting for k6 to complete..."
$k6Output = Receive-Job -Job $k6Job -Wait
Remove-Job -Job $k6Job

# cleanup: unblock heartbeats
Write-Host ""
Write-Host "[CLEANUP] Unblocking heartbeats..."
Unblock-Heartbeats -Container $blindGateway

# wait for recovery
Write-Host "[CLEANUP] Waiting for system to recover..."
Start-Sleep -Seconds 15

# final consistency check
Write-Host ""
Write-Host "[CHECK] Final consistency check (after recovery)..."
$finalCounts = Test-Consistency -GatewayCount $gateways.Count
Write-Host ("[CHECK] Worker counts: [" + ($finalCounts -join ', ') + "]")
if (($finalCounts | Sort-Object -Unique).Count -eq 1) {
    Write-Host "[CHECK] System recovered - all gateways consistent"
} else {
    Write-Host "[WARN] System still inconsistent after recovery!"
}

Write-Host ""
Write-Host "=========================================="
Write-Host "Test Complete"
Write-Host "=========================================="
Write-Host "Blind gateway: $blindGateway"
Write-Host ("Initial counts: [" + ($initialCounts -join ', ') + "]")
Write-Host ("During test:    [" + ($postBlockCounts -join ', ') + "]")
Write-Host ("After recovery: [" + ($finalCounts -join ', ') + "]")
Write-Host ""
Write-Host "Results saved to: test/k6/outputs/split_brain_summary.json"

