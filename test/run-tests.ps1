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
    [string]$Namespace = 'geostreamdb',
    [switch]$SkipInfra = $false,
    [switch]$UseKubernetes = $false, # toggle between Docker Compose (default) and Kubernetes
    [int]$WarmupSeconds = 5 # delay after readiness before running tests
)

$ErrorActionPreference = "Stop"
$env:ENTRYPOINT_URL = $EntrypointUrl # load balancer is the entrypoint
$PortForwardJobs = @()

$GatewayClassName = "nginx"
$NgfVersion = "v2.4.2"
$NgfGatewayApiCrdUrl = "https://github.com/nginx/nginx-gateway-fabric/config/crd/gateway-api/standard?ref=$NgfVersion"
$NgfNamespace = "nginx-gateway"
$NgfRelease = "ngf"

$orchestratedTests = @(
    'gateway-worker-latency',
    'registry-disruption',
    'registry-latency',
    'split-brain',
    'worker-churn',
    'all',
    'all-orchestrated'
)
$NeedsChaosMesh = $orchestratedTests -contains $Test

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

# get project root directory (parent of test/ directory, based on file's location)
$ProjectRoot = Split-Path -Parent $PSScriptRoot

Write-Host "=== GeoStreamDB k6 Tests ===" -ForegroundColor Cyan
Write-Host "Test: $Test"
Write-Host "Workers: $Workers"
Write-Host "Gateways: $Gateways"
Write-Host "Entrypoint: $EntrypointUrl"
Write-Host "Output Directory: $OutputDir"
Write-Host "Project Root: $ProjectRoot"
Write-Host ""

# check k6 is installed
try {
    k6 version | Out-Null
} catch {
    Write-Host "k6 not found. Install from: https://k6.io/docs/get-started/installation/" -ForegroundColor Red
    exit 1
}

# start infrastructure / run tests / optional cleanup
try {
if (-not $SkipInfra) {
    if ($UseKubernetes) {
        Write-Host "Starting Kubernetes infrastructure with $Workers workers and $Gateways gateways..." -ForegroundColor Yellow
        
        # ensure minikube is running
        Write-Host "Ensuring minikube is running..." -ForegroundColor Yellow
        minikube start
        if ($LASTEXITCODE -ne 0) {
            Write-Host "minikube start failed. If using Hyper-V, run it with administrator privileges." -ForegroundColor Red
            exit 1
        }

        # ensure Gateway API CRDs and NGINX Gateway Fabric are installed
        Write-Host "Ensuring Gateway API CRDs are installed (NGF recommended bundle)..." -ForegroundColor Yellow
        kubectl kustomize $NgfGatewayApiCrdUrl | kubectl apply -f -
        if ($LASTEXITCODE -ne 0) {
            Write-Host "Failed to install Gateway API CRDs." -ForegroundColor Red
            exit 1
        }

        Write-Host "Ensuring NGINX Gateway Fabric is installed..." -ForegroundColor Yellow
        helm upgrade --install $NgfRelease oci://ghcr.io/nginx/charts/nginx-gateway-fabric --create-namespace -n $NgfNamespace
        if ($LASTEXITCODE -ne 0) {
            Write-Host "Failed to install/upgrade NGINX Gateway Fabric." -ForegroundColor Red
            exit 1
        }
        kubectl wait --for=condition=available deployment -n $NgfNamespace -l app.kubernetes.io/name=nginx-gateway-fabric --timeout=180s
        if ($LASTEXITCODE -ne 0) {
            Write-Host "NGINX Gateway Fabric controller did not become available" -ForegroundColor Red
            exit 1
        }
        $gatewayClass = kubectl get gatewayclass $GatewayClassName -o name --ignore-not-found 2>$null
        if (-not $gatewayClass) {
            Write-Host ("GatewayClass '" + $GatewayClassName + "' not found. Available classes:") -ForegroundColor Red
            kubectl get gatewayclass
            exit 1
        }

        # install Chaos Mesh only when running orchestrated tests
        if ($NeedsChaosMesh) {
            $chaosNs = kubectl get ns chaos-mesh -o name --ignore-not-found 2>$null
            if (-not $chaosNs) {
                Write-Host "Installing Chaos Mesh..." -ForegroundColor Yellow
                helm repo add chaos-mesh https://charts.chaos-mesh.org 2>$null
                helm repo update 2>$null
                kubectl create namespace chaos-mesh
                helm install chaos-mesh chaos-mesh/chaos-mesh -n chaos-mesh --version 2.8.1
            }
        } else {
            Write-Host "Skipping Chaos Mesh (not needed for this test)" -ForegroundColor DarkGray
        }
        
        # change to project root for Docker builds and kubectl
        Push-Location $ProjectRoot

        # ensure namespace exists before creating namespaced resources
        $nsExists = kubectl get namespace $Namespace -o name --ignore-not-found 2>$null
        if (-not $nsExists) {
            kubectl create namespace $Namespace | Out-Null
        }

        # create/update grafana admin secret
        kubectl create secret generic grafana-admin --from-env-file=grafana-admin.env -n $Namespace --dry-run=client -o yaml | kubectl apply -f -
        
        try {
            # build images
            Write-Host "Building images..." -ForegroundColor Yellow
            docker build -f gateway/Dockerfile.multistage -t geostreamdb-gateway:latest .
            docker build -f worker-node/Dockerfile.multistage -t geostreamdb-worker-node:latest .
            docker build -f registry/Dockerfile.multistage -t geostreamdb-registry:latest .
        try {
            Write-Host "Loading images into minikube..." -ForegroundColor Yellow
            # load local images
            minikube image load geostreamdb-gateway:latest 2>&1 | Out-Null
            minikube image load geostreamdb-worker-node:latest 2>&1 | Out-Null
            minikube image load geostreamdb-registry:latest 2>&1 | Out-Null

            # load public images
            minikube image load prom/prometheus:v3.9.1 2>&1 | Out-Null
            minikube image load grafana/grafana:12.3.3 2>&1 | Out-Null
            minikube image load prom/node-exporter:v1.10.2 2>&1 | Out-Null
            minikube image load ghcr.io/davidborzek/docker-exporter:v0.3.0 2>&1 | Out-Null
        } catch {
            Write-Host "Warning: Could not load images. Make sure minikube or kind is running." -ForegroundColor Yellow
        }
        
        # apply Kubernetes manifests (kustomization.yaml)
        kubectl kustomize overlays/minikube --load-restrictor=LoadRestrictionsNone | kubectl apply -n $Namespace -f -
        
        # scale deployments
        kubectl scale deployment worker-node --replicas=$Workers -n $Namespace
        kubectl scale deployment gateway --replicas=$Gateways -n $Namespace
        
        Write-Host "Waiting for pods to be ready..." -ForegroundColor Yellow
        kubectl wait --for=condition=ready pod -l app=gateway -n $Namespace --timeout=120s
        kubectl wait --for=condition=ready pod -l app=worker-node -n $Namespace --timeout=120s
        kubectl wait --for=condition=ready pod -l app=registry -n $Namespace --timeout=120s
        kubectl wait --for=condition=ready pod -l app=prometheus -n $Namespace --timeout=120s
        kubectl wait --for=condition=ready pod -l app=grafana -n $Namespace --timeout=120s
        kubectl wait --for=condition=Programmed gateway/geostreamdb-gateway -n $Namespace --timeout=180s

        # wait for NGF data-plane pod created and ready before port-forwarding 8080
        $gatewayPodSelector = "gateway.networking.k8s.io/gateway-name=geostreamdb-gateway"
        kubectl wait --for=create pod -n $Namespace -l $gatewayPodSelector --timeout=120s
        if ($LASTEXITCODE -ne 0) {
            Write-Host "Gateway data-plane pod was not created in time" -ForegroundColor Red
            kubectl get svc -n $Namespace -l $gatewayPodSelector -o wide 2>$null
            kubectl get pod -n $Namespace -l $gatewayPodSelector -o wide 2>$null
            exit 1
        }
        kubectl wait --for=condition=ready pod -n $Namespace -l $gatewayPodSelector --timeout=180s
        if ($LASTEXITCODE -ne 0) {
            Write-Host "Gateway data-plane pod did not become ready in time" -ForegroundColor Red
            kubectl get pod -n $Namespace -l $gatewayPodSelector -o wide 2>$null
            exit 1
        }
        
        # port-forward services for local access during tests (localhost:8080/9090/3000)
        Write-Host "Starting Kubernetes port-forwards..." -ForegroundColor Yellow
        $requiredPorts = @(8080, 9090, 3000)
        $busyPorts = @()
        foreach ($p in $requiredPorts) {
            $listener = Get-NetTCPConnection -LocalPort $p -State Listen -ErrorAction SilentlyContinue
            if ($listener) {
                $busyPorts += $p
            }
        }
        if ($busyPorts.Count -gt 0) {
            Write-Host ("Cannot start port-forwards: localhost ports already in use: " + ($busyPorts -join ", ")) -ForegroundColor Red
            Write-Host "Stop conflicting local processes (or change EntrypointUrl/ports) and retry." -ForegroundColor Red
            exit 1
        }

        $PortForwardJobs += Start-Job -Name "pf-ngf-8080" -ScriptBlock {
            param($ns)
            kubectl port-forward -n $ns svc/geostreamdb-gateway-nginx 8080:80 2>&1
        } -ArgumentList $Namespace
        $PortForwardJobs += Start-Job -Name "pf-prometheus-9090" -ScriptBlock {
            param($ns)
            kubectl port-forward -n $ns svc/prometheus-service 9090:9090 2>&1
        } -ArgumentList $Namespace
        $PortForwardJobs += Start-Job -Name "pf-grafana-3000" -ScriptBlock {
            param($ns)
            kubectl port-forward -n $ns svc/grafana-service 3000:3000 2>&1
        } -ArgumentList $Namespace

        # verify local ports are bound. retry for startup lag before failing
        $pendingPorts = [System.Collections.ArrayList]::new()
        foreach ($p in $requiredPorts) { [void]$pendingPorts.Add($p) }
        $deadline = (Get-Date).AddSeconds(30)
        while ($pendingPorts.Count -gt 0 -and (Get-Date) -lt $deadline) {
            $toRemove = @()
            foreach ($p in $pendingPorts) {
                $listener = Get-NetTCPConnection -LocalPort $p -State Listen -ErrorAction SilentlyContinue
                if ($listener) {
                    $toRemove += $p
                }
            }
            foreach ($p in $toRemove) { [void]$pendingPorts.Remove($p) }
            if ($pendingPorts.Count -gt 0) {
                Start-Sleep -Seconds 1
            }
        }
        if ($pendingPorts.Count -gt 0) {
            Write-Host ("Port-forward check failed: localhost ports not listening after timeout: " + ($pendingPorts -join ", ")) -ForegroundColor Red
            foreach ($job in $PortForwardJobs) {
                $jobOut = Receive-Job -Job $job -Keep -ErrorAction SilentlyContinue
                if ($jobOut) {
                    Write-Host ("Port-forward job output (" + $job.Name + "): " + ($jobOut -join " ")) -ForegroundColor DarkYellow
                }
            }
            Write-Host "Ensure no local process is using those ports and retry." -ForegroundColor Red
            foreach ($job in $PortForwardJobs) {
                Stop-Job -Job $job -ErrorAction SilentlyContinue
                Remove-Job -Job $job -Force -ErrorAction SilentlyContinue
            }
            exit 1
        }

        Write-Host "Waiting for services..." -ForegroundColor Yellow
        $isReady = $false
        for ($i = 0; $i -lt 60; $i++) {
            try {
                $response = Invoke-WebRequest -Uri "$EntrypointUrl/ping?lat=0&lng=0" -Method GET -TimeoutSec 2 -ErrorAction SilentlyContinue
                if ($response.StatusCode -eq 200) {
                    Write-Host "Ready!" -ForegroundColor Green
                    $isReady = $true
                    break
                }
            } catch {}
            Start-Sleep -Seconds 2
            Write-Host "." -NoNewline
        }
        Write-Host ""
        if (-not $isReady) {
            Write-Host ("Service readiness check failed for " + $EntrypointUrl + " after timeout") -ForegroundColor Red
            foreach ($job in $PortForwardJobs) {
                $jobOut = Receive-Job -Job $job -Keep -ErrorAction SilentlyContinue
                if ($jobOut) {
                    Write-Host ("Port-forward job output (" + $job.Name + "): " + ($jobOut -join " ")) -ForegroundColor DarkYellow
                }
            }
            exit 1
        }
        } finally {
            Pop-Location # back to original location
        }
    } else {
        Write-Host "Starting Docker Compose infrastructure with $Workers workers and $Gateways gateways..." -ForegroundColor Yellow
        Push-Location $ProjectRoot
        try {
            docker compose up --build --scale worker-node=$Workers --scale gateway=$Gateways -d
        } finally {
            Pop-Location
        }
        
        Write-Host "Waiting for services..." -ForegroundColor Yellow
        for ($i = 0; $i -lt 30; $i++) {
            try {
                $response = Invoke-WebRequest -Uri "$EntrypointUrl/ping?lat=0&lng=0" -Method GET -TimeoutSec 2 -ErrorAction SilentlyContinue
                if ($response.StatusCode -eq 200) {
                    Write-Host "Ready!" -ForegroundColor Green
                    break
                }
            } catch {}
            Start-Sleep -Seconds 1
            Write-Host "." -NoNewline
        }
        Write-Host ""
    }

    if ($WarmupSeconds -gt 0) {
        Write-Host "Warming up for ${WarmupSeconds}s..." -ForegroundColor Yellow
        Start-Sleep -Seconds $WarmupSeconds
    }
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
        $params = @{}
        if ($UseKubernetes) {
            $params['UseKubernetes'] = $true
            $params['Namespace'] = $Namespace
        }
        & $scriptPath @params
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
                if ($UseKubernetes) {
                    $deploymentsToRestart = @("registry", "gateway", "worker-node")
                    foreach ($dep in $deploymentsToRestart) {
                        kubectl rollout restart deployment/$dep -n $Namespace
                        kubectl rollout status deployment/$dep -n $Namespace --timeout=180s
                    }
                } else {
                    Push-Location $ProjectRoot
                    try {
                        docker compose restart
                    } finally {
                        Pop-Location
                    }
                }
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
                if ($UseKubernetes) {
                    $deploymentsToRestart = @("registry", "gateway", "worker-node")
                    foreach ($dep in $deploymentsToRestart) {
                        kubectl rollout restart deployment/$dep -n $Namespace
                        kubectl rollout status deployment/$dep -n $Namespace --timeout=180s
                    }
                } else {
                    Push-Location $ProjectRoot
                    try {
                        docker compose restart
                    } finally {
                        Pop-Location
                    }
                }
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
    if ($UseKubernetes) {
        $cleanup = Read-Host "Scale down deployments? (y/N)"
        if ($cleanup -eq "y") {
            kubectl scale deployment worker-node --replicas=0 -n $Namespace
            kubectl scale deployment gateway --replicas=0 -n $Namespace
            kubectl scale deployment registry --replicas=0 -n $Namespace
        }

        $deleteCluster = Read-Host "Delete minikube cluster (--all)? (y/N)"
        if ($deleteCluster -eq "y") {
            minikube delete --all
        }
    } else {
        $cleanup = Read-Host "Stop containers? (y/N)"
        if ($cleanup -eq "y") {
            Push-Location $ProjectRoot
            try {
                docker compose down
            } finally {
                Pop-Location
            }
        }
    }
}
} finally {
    # always clean up background port-forward jobs started by this script
    if ($PortForwardJobs.Count -gt 0) {
        Write-Host "Stopping Kubernetes port-forwards..." -ForegroundColor Yellow
        foreach ($job in $PortForwardJobs) {
            Stop-Job -Job $job -ErrorAction SilentlyContinue
            Remove-Job -Job $job -Force -ErrorAction SilentlyContinue
        }
    }
}