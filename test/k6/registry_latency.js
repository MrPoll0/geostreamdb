import http from 'k6/http';
import { check, sleep } from 'k6';
import { Rate, Trend, Counter, Gauge } from 'k6/metrics';

const DURATION = __ENV.DURATION || '3m';

// functionality/measures shouldn't change at all unless
// the injected latency is higher than the heartbeat TTL
// in which case, workers would be evicted from the ring
// and ping writes would fail (no workers available) and 
// ping area queries would return empty results

export const options = {
    scenarios: {
        load: {
            executor: 'constant-arrival-rate',
            duration: DURATION,
            rate: 200,
            preAllocatedVUs: 30,
            timeUnit: '1s',
            maxVUs: 80,
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
        write_success: ['rate>0.95'],              // 95% success even with latency
        http_req_failed: ['rate<0.10'],            // <10% HTTP failures
        request_duration: ['p(95)<3000'],          // p95 under 3s
        request_duration: ['p(99)<5000'],          // p99 under 5s
        workers_available: ['rate>0.95'],          // workers visible 95%+ of the time despite heartbeat latency
        correctness_success: ['rate>0.95'],
    }
}

const BASE_URL = __ENV.ENTRYPOINT_URL || 'http://localhost:8080'
const REFLECT_DELAY = parseFloat(__ENV.REFLECT_DELAY) || 0.5

// metrics
const writeSuccess = new Rate('write_success');
const requestDuration = new Trend('request_duration');
const correctnessSuccess = new Rate('correctness_success');
const workerCount = new Gauge('worker_count');
const workersAvailable = new Rate('workers_available');
const serviceUnavailable = new Counter('service_unavailable_errors');
const timeoutErrors = new Counter('timeout_errors');

// load scenario: steady writes, track duration
export function load() {
    let lat = Math.random() * 160 - 80;
    let lng = Math.random() * 340 - 170;
    
    let start = Date.now();
    let res = http.post(`${BASE_URL}/ping`, JSON.stringify({lat: lat, lng: lng}), {
        tags: { name: 'POST /ping' },
        timeout: '10s',
    });
    let duration = Date.now() - start;
    
    let success = res.status === 201;
    writeSuccess.add(success);
    requestDuration.add(duration);
    
    if (res.status === 503) {
        serviceUnavailable.add(1);
    }
    if (res.status === 0) {
        timeoutErrors.add(1);
    }
    
    check(res, { 
        'write: status 201': () => res.status === 201,
        'write: duration under 5s': () => duration < 5000,
    });
}

// monitor scenario: check worker visibility
export function monitor() {
    let res = http.get(`${BASE_URL}/metrics`, {
        tags: { name: 'GET /metrics' },
        timeout: '5s',
    });
    
    if (res.status !== 200) {
        console.log(`monitor: failed to fetch metrics (status ${res.status})`);
        workersAvailable.add(false);
        return;
    }
    
    let lines = res.body.split('\n');
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
    console.log(`monitor: ${count} workers active`);
}

// correctness scenario: write then verify read
export function correctness() {
    let uniqueLat = 85 + Math.random() * 4;
    let uniqueLng = 170 + Math.random() * 8;
    let bbox = {
        minLat: uniqueLat - 0.001,
        maxLat: uniqueLat + 0.001,
        minLng: uniqueLng - 0.001,
        maxLng: uniqueLng + 0.001,
    };

    let beforeRes = http.get(`${BASE_URL}/pingArea?precision=8&minLat=${bbox.minLat}&maxLat=${bbox.maxLat}&minLng=${bbox.minLng}&maxLng=${bbox.maxLng}`, {
        tags: { name: 'GET /pingArea' },
        timeout: '10s',
    });
    if (beforeRes.status !== 200) {
        return;
    }
    let countBefore = getCount(beforeRes);

    let pingRes = http.post(`${BASE_URL}/ping`, JSON.stringify({lat: uniqueLat, lng: uniqueLng}), {
        tags: { name: 'POST /ping' },
        timeout: '10s',
    });
    if (pingRes.status !== 201) {
        return;
    }

    sleep(REFLECT_DELAY);

    let afterRes = http.get(`${BASE_URL}/pingArea?precision=8&minLat=${bbox.minLat}&maxLat=${bbox.maxLat}&minLng=${bbox.minLng}&maxLng=${bbox.maxLng}`, {
        tags: { name: 'GET /pingArea' },
        timeout: '10s',
    });
    if (afterRes.status !== 200) {
        return;
    }
    let countAfter = getCount(afterRes);

    let valid = check(countAfter, {
        'correctness: count increased by 1': () => countAfter === countBefore + 1
    });
    correctnessSuccess.add(valid);
}

export function handleSummary(data) {
    return {
        'stdout': JSON.stringify(data.metrics, null, 2),
        'outputs/registry_latency_summary.json': JSON.stringify(data, null, 2),
    }
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

