import http from 'k6/http';
import { check, sleep } from 'k6';
import { Rate, Trend, Counter, Gauge } from 'k6/metrics';

const DURATION = __ENV.DURATION || '3m';

export const options = {
    scenarios: {
        load: {
            executor: 'constant-arrival-rate',
            duration: DURATION,
            rate: 150,  // lower rate since requests will be slower
            preAllocatedVUs: 40,
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
        write_success: ['rate>0.90'],              // 90% success (timeouts expected)
        http_req_failed: ['rate<0.15'],            // <15% HTTP failures
        write_duration: ['p(95)<4000'],            // p95 under 4s
        write_duration: ['p(99)<6000'],            // p99 under 6s
        read_duration: ['p(95)<4000'],             // reads also affected
        workers_available: ['rate>0.99'],
        correctness_success: ['rate>0.90'],
    }
}

const BASE_URL = __ENV.ENTRYPOINT_URL || 'http://localhost:8080'
const REFLECT_DELAY = parseFloat(__ENV.REFLECT_DELAY) || 1.0  // longer delay for latency test

// metrics
const writeSuccess = new Rate('write_success');
const writeDuration = new Trend('write_duration');
const readDuration = new Trend('read_duration');
const correctnessSuccess = new Rate('correctness_success');
const workerCount = new Gauge('worker_count');
const workersAvailable = new Rate('workers_available');
const timeoutErrors = new Counter('timeout_errors');

// load scenario: writes with duration tracking
export function load() {
    let lat = Math.random() * 160 - 80;
    let lng = Math.random() * 340 - 170;
    
    let start = Date.now();
    let res = http.post(`${BASE_URL}/ping`, JSON.stringify({lat: lat, lng: lng}), {
        tags: { name: 'POST /ping' },
        timeout: '15s',  // higher timeout for latency
    });
    let duration = Date.now() - start;
    
    let success = res.status === 201;
    writeSuccess.add(success);
    writeDuration.add(duration);
    
    if (res.status === 0) {
        timeoutErrors.add(1);
    }
    
    check(res, { 
        'write: status 201': () => res.status === 201,
        'write: duration under 6s': () => duration < 6000,
    });
}

// monitor scenario: check worker visibility (should be unaffected)
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

    // get count before
    let beforeStart = Date.now();
    let beforeRes = http.get(`${BASE_URL}/pingArea?precision=8&minLat=${bbox.minLat}&maxLat=${bbox.maxLat}&minLng=${bbox.minLng}&maxLng=${bbox.maxLng}`, {
        tags: { name: 'GET /pingArea' },
        timeout: '15s',
    });
    readDuration.add(Date.now() - beforeStart);
    
    if (beforeRes.status !== 200) {
        return;
    }
    let countBefore = getCount(beforeRes);

    // send ping
    let pingRes = http.post(`${BASE_URL}/ping`, JSON.stringify({lat: uniqueLat, lng: uniqueLng}), {
        tags: { name: 'POST /ping' },
        timeout: '15s',
    });
    if (pingRes.status !== 201) {
        return;
    }

    sleep(REFLECT_DELAY);

    // get count after
    let afterStart = Date.now();
    let afterRes = http.get(`${BASE_URL}/pingArea?precision=8&minLat=${bbox.minLat}&maxLat=${bbox.maxLat}&minLng=${bbox.minLng}&maxLng=${bbox.maxLng}`, {
        tags: { name: 'GET /pingArea' },
        timeout: '15s',
    });
    readDuration.add(Date.now() - afterStart);
    
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
        'gateway_worker_latency_summary.json': JSON.stringify(data, null, 2),
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

