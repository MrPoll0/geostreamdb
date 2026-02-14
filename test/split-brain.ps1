# Tests split-brain condition by blocking heartbeats to one gateway
# Uses Chaos Mesh NetworkChaos for Kubernetes, iptables for Docker Compose
# This causes that gateway to lose all workers after TTL expires,
# creating inconsistent state across gateways

param(
    [int]$TestDurationMinutes = 2,
    [int]$WaitForTTLSeconds = 15,          # wait for workers to expire (TTL = 10s)
    [string]$GatewayPrefix = "geostreamdb-gateway",
    [string]$Namespace = "geostreamdb",
    [switch]$UseKubernetes = $false # toggle between Docker Compose (default) and Kubernetes
)

$ErrorActionPreference = "Stop"
$ChaosResourceName = "split-brain-test"

# get all gateway containers/pods
function Get-Gateways {
    if ($UseKubernetes) {
        $pods = kubectl get pods -n $Namespace -l app=gateway --field-selector=status.phase=Running -o jsonpath='{.items[*].metadata.name}' 2>$null
        if ($pods) {
            return @($pods -split ' ' | Where-Object { $_ })
        }
        return @()
    } else {
        $gateways = docker ps --filter "name=$GatewayPrefix" --format "{{.Names}}" 2>$null
        if ($gateways) {
            return @($gateways -split "`n" | Where-Object { $_ })
        }
        return @()
    }
}

# check if Chaos Mesh is installed
function Test-ChaosMesh {
    $crd = kubectl get crd networkchaos.chaos-mesh.org 2>$null
    return $LASTEXITCODE -eq 0
}

# block heartbeats to a gateway using Chaos Mesh (network partition)
function Block-Heartbeats-ChaosMesh {
    param([string]$GatewayPod)

    # block traffic from registry to the specific gateway pod
    # this simulates the gateway being unable to receive heartbeats
    $chaosYaml = @"
apiVersion: chaos-mesh.org/v1alpha1
kind: NetworkChaos
metadata:
  name: $ChaosResourceName
  namespace: $Namespace
spec:
  action: partition
  mode: one
  selector:
    namespaces:
      - $Namespace
    labelSelectors:
      app: gateway
    fieldSelectors:
      metadata.name: $GatewayPod
  direction: from
  target:
    mode: all
    selector:
      namespaces:
        - $Namespace
      labelSelectors:
        app: registry
"@

    $applyOutput = $chaosYaml | kubectl apply -f - 2>&1
    if ($LASTEXITCODE -ne 0) {
        Write-Host "[ERROR] Failed to create NetworkChaos: $applyOutput"
        return $false
    }

    Start-Sleep -Seconds 2
    $status = kubectl get networkchaos $ChaosResourceName -n $Namespace -o jsonpath='{.status.conditions[0].type}' 2>$null
    Write-Host "[BLOCK] Chaos Mesh partition created (status: $status)"
    return $true
}

# unblock heartbeats using Chaos Mesh
function Unblock-Heartbeats-ChaosMesh {
    kubectl delete networkchaos $ChaosResourceName -n $Namespace --ignore-not-found=true 2>&1 | Out-Null
    Start-Sleep -Seconds 2
    Write-Host "[UNBLOCK] Chaos Mesh partition removed"
}

# get container PID (Docker Compose only)
function Get-ContainerPid {
    param([string]$Container)
    $cPid = docker inspect -f '{{.State.Pid}}' $Container 2>$null
    if ($LASTEXITCODE -ne 0 -or -not $cPid) {
        return $null
    }
    return $cPid
}

# block heartbeats from registry to a gateway using iptables (drop packets from registry's gRPC port) (Docker Compose only)
function Block-Heartbeats-Docker {
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

# unblock heartbeats using iptables (Docker Compose only)
function Unblock-Heartbeats-Docker {
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

# quick consistency check
function Test-Consistency {
    param([int]$GatewayCount)

    $entrypoint = $env:ENTRYPOINT_URL
    if (-not $entrypoint) { $entrypoint = "http://localhost:8080" }
    $metricsUrl = $entrypoint.TrimEnd('/') + '/metrics'

    $counts = @()
    for ($i = 0; $i -lt $GatewayCount; $i++) {
        try {
            $response = Invoke-RestMethod -Uri $metricsUrl -TimeoutSec 5
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

# check Chaos Mesh is installed (Kubernetes only)
if ($UseKubernetes) {
    Write-Host "[INIT] Checking Chaos Mesh installation..."
    if (-not (Test-ChaosMesh)) {
        Write-Host "[ERROR] Chaos Mesh is not installed. Install it with:"
        Write-Host "  kubectl create ns chaos-mesh"
        Write-Host "  helm repo add chaos-mesh https://charts.chaos-mesh.org"
        Write-Host "  helm install chaos-mesh chaos-mesh/chaos-mesh -n chaos-mesh --set chaosDaemon.runtime=containerd --set chaosDaemon.socketPath=/run/containerd/containerd.sock"
        exit 1
    }
    Write-Host "[INIT] Chaos Mesh is installed"
} else {
    Write-Host "[INIT] Pulling alpine image..."
    docker pull alpine 2>&1 | Out-Null
}

# get gateways
Write-Host "[INIT] Finding gateway containers/pods..."
$gateways = Get-Gateways
if ($gateways.Count -eq 0) {
    Write-Host "[ERROR] No gateway containers/pods found"
    if ($UseKubernetes) {
        Write-Host "[ERROR] Make sure the system is up (from project root): kubectl apply -k ."
    }
    exit 1
}
Write-Host "[INIT] Found $($gateways.Count) gateways: $($gateways -join ', ')"

# select one gateway to make "blind"
$blindGateway = $gateways[0]
Write-Host "[INIT] Target gateway (will become blind): $blindGateway"

# cleanup any existing chaos
if ($UseKubernetes) {
    Unblock-Heartbeats-ChaosMesh
}

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
if ($UseKubernetes) {
    if (-not (Block-Heartbeats-ChaosMesh -GatewayPod $blindGateway)) {
        Write-Host "[ERROR] Failed to block heartbeats"
        exit 1
    }
} else {
    if (-not (Block-Heartbeats-Docker -Container $blindGateway)) {
        Write-Host "[ERROR] Failed to block heartbeats"
        exit 1
    }
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
$k6Dir = Join-Path $PSScriptRoot "k6"
$entrypoint = $env:ENTRYPOINT_URL
if (-not $entrypoint) { $entrypoint = "http://localhost:8080" }
$k6Job = Start-Job -ScriptBlock {
    param($duration, $gatewayCount, $dir, $url)
    Set-Location $dir
    k6 run --env DURATION="${duration}m" --env GATEWAY_COUNT=$gatewayCount --env ENTRYPOINT_URL=$url split_brain.js 2>&1
} -ArgumentList $TestDurationMinutes, $gateways.Count, $k6Dir, $entrypoint

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
if ($UseKubernetes) {
    Unblock-Heartbeats-ChaosMesh
} else {
    Unblock-Heartbeats-Docker -Container $blindGateway
}

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