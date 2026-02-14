# Runs k6 load test while injecting network latency on worker containers
# This affects the gateway<->worker data path (ping writes and reads)
# Uses tc/netem for Docker Compose, and Chaos Mesh NetworkChaos for Kubernetes

param(
    [int]$LatencyMs = 500,                  # latency to inject (ms)
    [int]$JitterMs = 100,                   # jitter/variance (ms)
    [int]$LatencyIntervalSeconds = 30,      # how often to toggle latency
    [int]$LatencyDurationSeconds = 20,      # how long latency is active per cycle
    [int]$TestDurationMinutes = 3,          # total test duration
    [string]$WorkerPrefix = "geostreamdb-worker",  # worker container name prefix
    [string]$Namespace = "geostreamdb",
    [switch]$UseKubernetes = $false # toggle between Docker Compose (default) and Kubernetes
)

$ErrorActionPreference = "Stop"
$ChaosMeshNamespace = "chaos-mesh"
$ChaosResourceName = "worker-latency-test"

# get all worker containers/pods
function Get-Workers {
    if ($UseKubernetes) {
        $pods = kubectl get pods -n $Namespace -l app=worker-node --field-selector=status.phase=Running -o jsonpath='{.items[*].metadata.name}' 2>$null
        if ($pods) {
            return @($pods -split ' ' | Where-Object { $_ })
        }
        return @()
    } else {
        $workers = docker ps --filter "name=$WorkerPrefix" --format "{{.Names}}" 2>$null
        if ($workers) {
            return @($workers -split "`n" | Where-Object { $_ })
        }
        return @()
    }
}

# check if Chaos Mesh is installed
function Test-ChaosMesh {
    $crd = kubectl get crd networkchaos.chaos-mesh.org 2>$null
    return $LASTEXITCODE -eq 0
}

# clean up any existing NetworkChaos resources
function Remove-ChaosMeshLatency {
    if ($UseKubernetes) {
        # check if resource exists first
        $exists = kubectl get networkchaos $ChaosResourceName -n $Namespace -o name --ignore-not-found=true 2>$null
        if ($exists) {
            Write-Host "[CHAOS] Deleting NetworkChaos resource: $ChaosResourceName"
            $deleteOutput = kubectl delete networkchaos $ChaosResourceName -n $Namespace --wait=true 2>&1
            if ($LASTEXITCODE -ne 0) {
                Write-Host "[WARN] Delete may have failed: $deleteOutput"
            }
            # wait for Chaos Mesh daemon to propagate removal to all pods
            Write-Host "[CHAOS] Waiting for network rules to be removed from pods..."
            Start-Sleep -Seconds 5
            # verify it's gone
            $stillExists = kubectl get networkchaos $ChaosResourceName -n $Namespace -o name --ignore-not-found=true 2>$null
            if ($stillExists) {
                Write-Host "[ERROR] NetworkChaos resource still exists after delete!"
                # force delete
                kubectl delete networkchaos $ChaosResourceName -n $Namespace --force --grace-period=0 2>&1 | Out-Null
                Start-Sleep -Seconds 3
            } else {
                Write-Host "[CHAOS] NetworkChaos resource deleted, rules should be cleared"
            }
        } else {
            Write-Host "[CHAOS] No NetworkChaos resource to delete"
        }
    }
}

# inject latency using Chaos Mesh NetworkChaos
function Add-ChaosMeshLatency {
    param([int]$LatencyMs, [int]$JitterMs)

    $chaosYaml = @"
apiVersion: chaos-mesh.org/v1alpha1
kind: NetworkChaos
metadata:
  name: $ChaosResourceName
  namespace: $Namespace
spec:
  action: delay
  duration: "${LatencyDurationSeconds}s"
  mode: all
  selector:
    namespaces:
      - $Namespace
    labelSelectors:
      app: worker-node
  delay:
    latency: "${LatencyMs}ms"
    jitter: "${JitterMs}ms"
    correlation: "50"
  direction: to
"@

    $applyOutput = $chaosYaml | kubectl apply -f - 2>&1
    if ($LASTEXITCODE -ne 0) {
        Write-Host "[ERROR] Failed to create NetworkChaos: $applyOutput"
        return $false
    }

    # verify it was created
    Start-Sleep -Seconds 2
    $status = kubectl get networkchaos $ChaosResourceName -n $Namespace -o jsonpath='{.status.conditions[0].type}' 2>$null
    Write-Host "[LATENCY] Chaos Mesh NetworkChaos created (status: $status, duration: ${LatencyDurationSeconds}s)"
    return $true
}

# get container PID for nsenter (Docker Compose only)
function Get-ContainerPid {
    param([string]$Container)
    $cPid = docker inspect -f '{{.State.Pid}}' $Container 2>$null
    if ($LASTEXITCODE -ne 0 -or -not $cPid) {
        return $null
    }
    return $cPid
}

# add network latency to a container (Docker Compose only)
function Add-NetworkDelay-Docker {
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

# remove network latency from a container (Docker Compose only)
function Remove-NetworkDelay-Docker {
    param([string]$Container)

    $cPid = Get-ContainerPid -Container $Container
    if (-not $cPid) {
        return $false
    }

    try {
        $ErrorActionPreference = "SilentlyContinue"
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

    if ($UseKubernetes) {
        return Add-ChaosMeshLatency -LatencyMs $LatencyMs -JitterMs $JitterMs
    } else {
        $workers = Get-Workers
        $successCount = 0
        foreach ($worker in $workers) {
            if (Add-NetworkDelay-Docker -Container $worker -LatencyMs $LatencyMs -JitterMs $JitterMs) {
                $successCount++
            }
        }
        Write-Host "[LATENCY] Added ${LatencyMs}ms +/- ${JitterMs}ms delay to $successCount/$($workers.Count) workers"
        return $successCount -gt 0
    }
}

# remove latency from all workers
function Remove-LatencyFromAllWorkers {
    if ($UseKubernetes) {
        Remove-ChaosMeshLatency
        Write-Host "[LATENCY] Chaos Mesh NetworkChaos removed"
    } else {
        $workers = Get-Workers
        $successCount = 0
        foreach ($worker in $workers) {
            if (Remove-NetworkDelay-Docker -Container $worker) {
                $successCount++
            }
        }
        Write-Host "[LATENCY] Removed delay from $successCount/$($workers.Count) workers"
    }
}

# main orchestration
Write-Host "=========================================="
Write-Host "Gateway-Worker Latency Test"
Write-Host "=========================================="
Write-Host "Latency: ${LatencyMs}ms +/- ${JitterMs}ms"
Write-Host "Latency interval: ${LatencyIntervalSeconds}s"
Write-Host "Latency duration: ${LatencyDurationSeconds}s per cycle"
Write-Host "Test duration: ${TestDurationMinutes}m"
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

# check workers exist
Write-Host "[INIT] Checking worker containers/pods..."
$workers = Get-Workers
if ($workers.Count -eq 0) {
    Write-Host "[ERROR] No worker containers/pods found"
    if ($UseKubernetes) {
        Write-Host "[ERROR] Make sure the system is up (from project root): kubectl apply -k ."
    } else {
        Write-Host "[ERROR] Make sure the system is up: docker-compose up -d"
    }
    exit 1
}
Write-Host "[INIT] Found $($workers.Count) workers: $($workers -join ', ')"

# clean up any existing latency
Write-Host "[INIT] Cleaning up any existing latency..."
Remove-LatencyFromAllWorkers
Start-Sleep -Seconds 3

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
Write-Host "[CLEANUP] Ensuring all latency is removed..."
Remove-LatencyFromAllWorkers

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