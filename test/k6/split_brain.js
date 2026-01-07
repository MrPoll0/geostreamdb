import http from 'k6/http';
import { check, sleep } from 'k6';
import { Rate, Counter, Gauge } from 'k6/metrics';

const DURATION = __ENV.DURATION || '2m';
const GATEWAY_COUNT = parseInt(__ENV.GATEWAY_COUNT) || 3;

// this test detects split-brain conditions where gateways have inconsistent
// views of workers due to heartbeat delivery failures

// the orchestration script blocks heartbeats to ONE gateway, causing it to
// lose all workers after TTL expires. this gateway becomes "blind"

// with round-robin LB, querying N times (N = gateway count) hits each gateway once
// we detect split-brain when worker counts differ across queries

// this test intentionally fails to prove a known vulnerability
// - registry is a single point of failure for heartbeat delivery
// - if heartbeats fail to reach a gateway, that gateway becomes "blind" (evicts all workers once heartbeat TTL expires)
// - blind gateways fail all writes (503) and return empty reads

// this issue could lead to the following data loss condition:
// T=0: New worker C joins, heartbeats to registry
// T=1: Registry broadcasts to gateway 1 (good)
//      Registry broadcasts to gateway 2 (slow/failed)
// T=2: Request arrives at gateway 2
//      Given geohash would route to worker C, but gateway 2 doesn't know C exists yet,
//      thus routes to worker A instead
// T=3: Gateway 2 finally receives heartbeat, includes worker C in ring
// T=4: New request for the same geohash prefix now routes to worker C
//      Data written to A is now unreachable, and queries to C return empty

// registry replication may mitigate this issue due to redundant heartbeats from all replicas

export const options = {
    scenarios: {
        // continuous monitoring to detect inconsistency
        consistency_check: {
            executor: 'constant-arrival-rate',
            duration: DURATION,
            rate: 1,
            preAllocatedVUs: 1,
            timeUnit: '1s',
            maxVUs: 2,
            exec: 'consistencyCheck',
        },
        // load test to measure impact
        load: {
            executor: 'constant-arrival-rate',
            duration: DURATION,
            rate: 100,
            preAllocatedVUs: 20,
            timeUnit: '1s',
            maxVUs: 50,
            exec: 'load',
        },
    },
    thresholds: {
        // these thresholds will FAIL when split-brain exists (expected)
        // they become regression tests for when split-brain is fixed
        worker_count_consistent: ['rate>0.99'],   // all gateways should agree
        write_success: ['rate>0.90'],             // writes may fail on blind gateway
        blind_gateway_detected: ['count<1'],      // should never detect blind gateway
    }
}

const BASE_URL = __ENV.ENTRYPOINT_URL || 'http://localhost:8080'

// metrics
const workerCountConsistent = new Rate('worker_count_consistent');
const writeSuccess = new Rate('write_success');
const blindGatewayDetected = new Counter('blind_gateway_detected');
const workerCountMin = new Gauge('worker_count_min');
const workerCountMax = new Gauge('worker_count_max');

// consistency check: query each gateway via round-robin
export function consistencyCheck() {
    let counts = [];
    
    // query GATEWAY_COUNT times to hit each gateway once (round-robin)
    for (let i = 0; i < GATEWAY_COUNT; i++) {
        let res = http.get(`${BASE_URL}/metrics`, {
            tags: { name: 'GET /metrics' },
            timeout: '5s',
        });
        
        if (res.status === 200) {
            let count = parseWorkerCount(res.body);
            counts.push(count);
        } else {
            counts.push(-1);  // gateway unavailable
        }
    }
    
    // check consistency
    let validCounts = counts.filter(c => c >= 0);
    if (validCounts.length === 0) {
        console.log(`consistency: all gateways unavailable`);
        return;
    }
    
    let minCount = Math.min(...validCounts);
    let maxCount = Math.max(...validCounts);
    let isConsistent = minCount === maxCount;
    
    workerCountConsistent.add(isConsistent);
    workerCountMin.add(minCount);
    workerCountMax.add(maxCount);
    
    if (!isConsistent) {
        blindGatewayDetected.add(1);
        console.log(`SPLIT-BRAIN DETECTED: worker counts = [${counts.join(', ')}]`);
    } else {
        console.log(`consistency: all gateways report ${minCount} workers`);
    }
}

// load test: detect partial failures due to blind gateway
export function load() {
    let lat = Math.random() * 160 - 80;
    let lng = Math.random() * 340 - 170;
    
    let res = http.post(`${BASE_URL}/ping`, JSON.stringify({lat: lat, lng: lng}), {
        tags: { name: 'POST /ping' },
        timeout: '5s',
    });
    
    let success = res.status === 201;
    writeSuccess.add(success);
    
    check(res, {
        'write: status 201 or 503': () => res.status === 201 || res.status === 503,
    });
}

function parseWorkerCount(body) {
    let lines = body.split('\n');
    for (let line of lines) {
        if (line.startsWith('gateway_worker_nodes_total ')) {
            let parts = line.split(' ');
            if (parts.length >= 2) {
                return parseInt(parts[1]) || 0;
            }
        }
    }
    return 0;
}

export function handleSummary(data) {
    // extract key metrics for summary
    let metrics = data.metrics;
    let splitBrainDetected = metrics.blind_gateway_detected && 
                              metrics.blind_gateway_detected.values.count > 0;
    
    let summary = {
        split_brain_detected: splitBrainDetected,
        worker_count_min: metrics.worker_count_min ? metrics.worker_count_min.values.value : null,
        worker_count_max: metrics.worker_count_max ? metrics.worker_count_max.values.value : null,
        consistency_rate: metrics.worker_count_consistent ? 
                          metrics.worker_count_consistent.values.rate : null,
        write_success_rate: metrics.write_success ? 
                            metrics.write_success.values.rate : null,
    };
    
    console.log('\n========== SPLIT-BRAIN TEST SUMMARY ==========');
    console.log(`Split-brain detected: ${splitBrainDetected ? 'YES ⚠️' : 'NO ✅'}`);
    console.log(`Worker count range: ${summary.worker_count_min} - ${summary.worker_count_max}`);
    console.log(`Consistency rate: ${(summary.consistency_rate * 100).toFixed(1)}%`);
    console.log(`Write success rate: ${(summary.write_success_rate * 100).toFixed(1)}%`);
    console.log('================================================\n');
    
    return {
        'stdout': JSON.stringify(summary, null, 2),
        'split_brain_summary.json': JSON.stringify(data, null, 2),
    }
}

