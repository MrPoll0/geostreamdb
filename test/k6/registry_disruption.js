import http from 'k6/http';
import { check, sleep } from 'k6';
import { Rate, Gauge, Counter } from 'k6/metrics';

const DURATION = __ENV.DURATION || '3m';

// TODO: the current result of this test fails
// this is because the idea is for the registry to be replicated
// otherwise, the registry is a single point of failure, as it can be seen in this test

// with 15s disruption, and heartbeat TTL of 10s, after TTL expires, gateways consider 
// all workers dead, thus emptying the ring.
// all ping writes will fail (no workers available) and ping area queries will return empty results
// very similar failing rate of write_success and workers_available proves this

// with replicated registry, unless all replicas are down, any registry can be used as heartbeat proxy
// e.g. with a DNS to resolve all registry addresses, and a load balancer to route traffic to any available registry

export const options = {
    scenarios: {
        load: {
            executor: 'constant-arrival-rate',
            duration: DURATION,
            rate: 300,
            preAllocatedVUs: 30,
            timeUnit: '1s',
            maxVUs: 100,
            exec: 'load',
        },
        monitor: {
            executor: 'constant-arrival-rate',
            duration: DURATION,
            rate: 1,
            preAllocatedVUs: 1,
            timeUnit: '1s',
            maxVUs: 2,
            exec: 'monitor',
        },
        correctness: {
            executor: 'constant-arrival-rate',
            duration: DURATION,
            rate: 1,
            preAllocatedVUs: 2,
            timeUnit: '1s',
            maxVUs: 5,
            exec: 'correctness',
        }
    },
    thresholds: {
        // gateway should continue operating with cached workers (until heartbeat TTL expires and gateways consider all workers dead)
        write_success: ['rate>0.85'],           // up to 15% failures during disruption. ping writes may fail if no workers are available (heartbeat TTL expires and gateways consider all workers dead)
        http_req_failed: ['rate<0.20'],         // up to 20% HTTP failures
        correctness_success: ['rate>0.90'],     // correctness may degrade during disruption
        worker_count: ['value>0'],              // workers should remain visible (value is only the last recorded value)
        workers_available: ['rate>0.99'],        // workers exist 99% of the time (cached workers are still available until heartbeat TTL expires)
    }
}

const BASE_URL = __ENV.ENTRYPOINT_URL || 'http://localhost:8080'
const REFLECT_DELAY = parseFloat(__ENV.REFLECT_DELAY) || 0.5

// metrics
const writeSuccess = new Rate('write_success');
const correctnessSuccess = new Rate('correctness_success');
const workerCount = new Gauge('worker_count');
const serviceUnavailable = new Counter('service_unavailable_errors');
const connectionErrors = new Counter('connection_errors');
const gatewayHealthy = new Gauge('gateway_healthy');
const workersAvailable = new Rate('workers_available');

// load scenario: steady writes
export function load() {
    let lat = Math.random() * 160 - 80
    let lng = Math.random() * 340 - 170
    
    let res = http.post(`${BASE_URL}/ping`, JSON.stringify({lat: lat, lng: lng}), {
        tags: { name: 'POST /ping' },
        timeout: '5s',
    })
    
    let success = res.status === 201;
    writeSuccess.add(success);
    
    if (res.status === 503) {
        serviceUnavailable.add(1);
    }
    if (res.status === 0) {
        connectionErrors.add(1);
    }
    
    check(res, { 
        'write: status 201 or 503': () => res.status === 201 || res.status === 503 
    });
}

// monitor scenario: check gateway health and worker count
export function monitor() {
    // check gateway metrics endpoint (should work even if registry is down)
    let metricsRes = http.get(`${BASE_URL}/metrics`, {
        tags: { name: 'GET /metrics' },
        timeout: '2s',
    });
    
    let healthy = metricsRes.status === 200;
    gatewayHealthy.add(healthy ? 1 : 0);
    
    if (!healthy) {
        console.log(`monitor: gateway metrics unavailable (status ${metricsRes.status})`);
        return;
    }
    
    // parse worker count from gateway_worker_nodes_total gauge
    let lines = metricsRes.body.split('\n');
    let count = 0;
    
    for (let line of lines) {
        if (line.startsWith('gateway_worker_nodes_total ')) {
            let parts = line.split(' ');
            if (parts.length >= 2) {
                count = parseInt(parts[1]) || 0;
                break;
            }
        }
    }
    
    workerCount.add(count);
    workersAvailable.add(count > 0);
    console.log(`monitor: gateway healthy, ${count} workers`);
}

// correctness scenario: write then verify read
export function correctness() {
    let uniqueLat = 85 + Math.random() * 4
    let uniqueLng = 170 + Math.random() * 8
    let bbox = {
        minLat: uniqueLat - 0.001,
        maxLat: uniqueLat + 0.001,
        minLng: uniqueLng - 0.001,
        maxLng: uniqueLng + 0.001,
    }

    // get count before
    let beforeRes = http.get(`${BASE_URL}/pingArea?precision=8&minLat=${bbox.minLat}&maxLat=${bbox.maxLat}&minLng=${bbox.minLng}&maxLng=${bbox.maxLng}`, {
        tags: { name: 'GET /pingArea' },
        timeout: '5s',
    })
    if (beforeRes.status !== 200) {
        return;  // skip during disruption
    }
    let countBefore = getCount(beforeRes);

    // send ping
    let pingRes = http.post(`${BASE_URL}/ping`, JSON.stringify({lat: uniqueLat, lng: uniqueLng}), {
        tags: { name: 'POST /ping' },
        timeout: '5s',
    })
    if (pingRes.status !== 201) {
        return;  // skip during disruption
    }

    sleep(REFLECT_DELAY);

    // get count after
    let afterRes = http.get(`${BASE_URL}/pingArea?precision=8&minLat=${bbox.minLat}&maxLat=${bbox.maxLat}&minLng=${bbox.minLng}&maxLng=${bbox.maxLng}`, {
        tags: { name: 'GET /pingArea' },
        timeout: '5s',
    })
    if (afterRes.status !== 200) {
        return;
    }
    let countAfter = getCount(afterRes);

    let valid = check(countAfter, {
        'correctness: count increased by 1': () => countAfter === countBefore + 1
    });
    correctnessSuccess.add(valid);
}

function getCount(res) {
    try {
        const data = JSON.parse(res.body);
        let total = 0;
        for (const key in data) {
            if (data[key] && typeof data[key].Count === 'number') {
                total += data[key].Count;
            }
        }
        return total;
    } catch {
        return -1;
    }
}

export function handleSummary(data) {
    return {
        'stdout': JSON.stringify(data.metrics, null, 2),
        'registry_disruption_summary.json': JSON.stringify(data, null, 2),
    }
}