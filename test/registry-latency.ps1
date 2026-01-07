# Runs k6 load test while injecting network latency via Linux tc (traffic control)
# Uses nsenter from a privileged alpine container to access the target container's network namespace

param(
    [int]$LatencyMs = 500,                  # latency to inject (ms)
    [int]$JitterMs = 100,                   # jitter/variance (ms)
    [int]$LatencyIntervalSeconds = 30,      # how often to toggle latency
    [int]$LatencyDurationSeconds = 20,      # how long latency is active per cycle
    [int]$TestDurationMinutes = 3,          # total test duration
    [string]$TargetContainer = "registry"   # container to inject latency on
)

$ErrorActionPreference = "Stop"

# Get container PID for nsenter
function Get-ContainerPid {
    param([string]$Container)
    $cPid = docker inspect -f '{{.State.Pid}}' $Container 2>$null
    if ($LASTEXITCODE -ne 0 -or -not $cPid) {
        return $null
    }
    return $cPid
}

# Add network latency using tc via nsenter
function Add-NetworkDelay {
    param([string]$Container, [int]$LatencyMs, [int]$JitterMs)
    
    $cPid = Get-ContainerPid -Container $Container
    if (-not $cPid) {
        Write-Host "[ERROR] Could not get PID for container $Container"
        return $false
    }
    
    # Run tc in the container's network namespace using nsenter from alpine
    try {
        $ErrorActionPreference = "SilentlyContinue"
        $result = docker run --rm --privileged --pid=host alpine sh -c `
            "apk add --no-cache -q iproute2 >/dev/null 2>&1; nsenter -t $cPid -n tc qdisc add dev eth0 root netem delay ${LatencyMs}ms ${JitterMs}ms" 2>&1
        $exitCode = $LASTEXITCODE
        $ErrorActionPreference = "Stop"
    } catch {
        Write-Host "[ERROR] Failed to add latency: $_"
        return $false
    }
    
    if ($exitCode -eq 0) {
        Write-Host "[LATENCY] Added ${LatencyMs}ms +/- ${JitterMs}ms delay to $Container"
        return $true
    } else {
        Write-Host "[ERROR] Failed to add latency: $result"
        return $false
    }
}

# Remove network latency
function Remove-NetworkDelay {
    param([string]$Container)
    
    $cPid = Get-ContainerPid -Container $Container
    if (-not $cPid) {
        Write-Host "[ERROR] Could not get PID for container $Container"
        return $false
    }
    
    # Remove tc qdisc (ignore errors if no qdisc exists)
    try {
        $ErrorActionPreference = "SilentlyContinue"
        docker run --rm --privileged --pid=host alpine sh -c `
            "apk add --no-cache -q iproute2 >/dev/null 2>&1; nsenter -t $cPid -n tc qdisc del dev eth0 root 2>/dev/null; exit 0" 2>&1 | Out-Null
        $ErrorActionPreference = "Stop"
    } catch {
        # Ignore - qdisc might not exist
    }
    
    Write-Host "[LATENCY] Removed delay from $Container"
    return $true
}

# Check if latency is currently active
function Test-NetworkDelay {
    param([string]$Container)
    
    $cPid = Get-ContainerPid -Container $Container
    if (-not $cPid) {
        return $false
    }
    
    try {
        $ErrorActionPreference = "SilentlyContinue"
        $result = docker run --rm --privileged --pid=host alpine sh -c `
            "apk add --no-cache -q iproute2 >/dev/null 2>&1; nsenter -t $cPid -n tc qdisc show dev eth0" 2>&1
        $ErrorActionPreference = "Stop"
        return $result -match "netem"
    } catch {
        return $false
    }
}

# Main orchestration
Write-Host "=========================================="
Write-Host "Network Latency Test (tc/netem)"
Write-Host "=========================================="
Write-Host "Target container: $TargetContainer"
Write-Host "Latency: ${LatencyMs}ms +/- ${JitterMs}ms"
Write-Host "Latency interval: ${LatencyIntervalSeconds}s"
Write-Host "Latency duration: ${LatencyDurationSeconds}s per cycle"
Write-Host "Test duration: ${TestDurationMinutes}m"
Write-Host ""

# Pre-pull alpine image to avoid stderr noise during test
Write-Host "[INIT] Pulling alpine image..."
docker pull alpine 2>&1 | Out-Null

# Check if target container exists and is running
Write-Host "[INIT] Checking target container..."
$containerPid = Get-ContainerPid -Container $TargetContainer
if (-not $containerPid) {
    Write-Host "[ERROR] Container '$TargetContainer' not found or not running"
    Write-Host "[ERROR] Make sure the system is up: docker-compose up -d"
    exit 1
}
Write-Host "[INIT] Container $TargetContainer is running (PID: $containerPid)"

# Clean up any existing latency
Write-Host "[INIT] Cleaning up any existing latency..."
Remove-NetworkDelay -Container $TargetContainer | Out-Null

# Start k6 in background
Write-Host "[TEST] Starting k6 load test..."
$k6Job = Start-Job -ScriptBlock {
    param($duration)
    Set-Location $using:PWD
    k6 run --env DURATION="${duration}m" test/k6/registry_latency.js 2>&1
} -ArgumentList $TestDurationMinutes

# Wait for k6 to start
Start-Sleep -Seconds 5

# Latency injection loop
$endTime = (Get-Date).AddMinutes($TestDurationMinutes)
$cycleCount = 0
$latencyActive = $false

while ((Get-Date) -lt $endTime) {
    $remaining = [math]::Round(($endTime - (Get-Date)).TotalSeconds)
    Write-Host "[STATUS] Latency: $(if ($latencyActive) { 'ACTIVE' } else { 'OFF' }), Time remaining: ${remaining}s"
    
    if (-not $latencyActive) {
        # Wait before injecting latency
        $waitTime = [math]::Min($LatencyIntervalSeconds, $remaining)
        if ($waitTime -le 0) { break }
        
        Write-Host "[WAIT] Next latency injection in ${waitTime}s..."
        Start-Sleep -Seconds $waitTime
        
        # Check if we have time for a latency cycle
        $remaining = [math]::Round(($endTime - (Get-Date)).TotalSeconds)
        if ($remaining -lt ($LatencyDurationSeconds + 5)) {
            Write-Host "[SKIP] Not enough time for latency cycle, skipping"
            break
        }
        
        # Inject latency
        if (Add-NetworkDelay -Container $TargetContainer -LatencyMs $LatencyMs -JitterMs $JitterMs) {
            $latencyActive = $true
            $cycleCount++
        }
        
    } else {
        # Latency is active, wait for duration then remove
        Write-Host "[LATENCY] Latency active for ${LatencyDurationSeconds}s..."
        Start-Sleep -Seconds $LatencyDurationSeconds
        
        Remove-NetworkDelay -Container $TargetContainer
        $latencyActive = $false
        
        Write-Host "[RECOVERY] Latency removed, system recovering..."
        Start-Sleep -Seconds 5
    }
}

# Cleanup: ensure latency is removed
if ($latencyActive) {
    Remove-NetworkDelay -Container $TargetContainer
}

# Wait for k6 to finish
Write-Host "[TEST] Waiting for k6 to complete..."
$k6Output = Receive-Job -Job $k6Job -Wait
Remove-Job -Job $k6Job

Write-Host ""
Write-Host "=========================================="
Write-Host "Test Complete"
Write-Host "=========================================="
Write-Host "Latency cycles: $cycleCount"
Write-Host "Latency per cycle: ${LatencyMs}ms +/- ${JitterMs}ms"
Write-Host ""
Write-Host "Results saved to: test/k6/outputs/registry_latency_summary.json"

