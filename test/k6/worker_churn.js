import http from 'k6/http';
import { check, sleep } from 'k6';
import { Rate, Gauge, Counter } from 'k6/metrics';

const DURATION = __ENV.DURATION || '3m';

export const options = {
    scenarios: {
        load: {
            executor: 'constant-arrival-rate',
            duration: DURATION,
            rate: 300,  // lower rate to not overwhelm during churn
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
        // relaxed thresholds (brief errors during churn)
        write_success: ['rate>0.85'],           // up to 15% failures during churn. ping writes into a dead worker geohash will fail until the gateway considers it dead and refreshes the ring
        http_req_failed: ['rate<0.15'],         // up to 15% HTTP failures
        correctness_success: ['rate>0.95'],     // correctness should still mostly work
        worker_count: ['value>0'],              // at least 1 worker should always exist (value is only the last recorded value)
        workers_available: ['rate>0.99'],        // workers exist 99% of the time
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
    
    // track specific error types
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

// monitor scenario: check gateway worker count via metrics
export function monitor() {
    let res = http.get(`${BASE_URL}/metrics`, {
        tags: { name: 'GET /metrics' },
        timeout: '2s',
    });
    
    if (res.status !== 200) {
        console.log(`monitor: failed to fetch metrics (status ${res.status})`);
        return;
    }
    
    // parse worker count from gateway_worker_nodes_total gauge
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

// correctness scenario: write then verify read (in isolated region)
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
        // during churn, reads may fail, dont count as correctness failure
        return;
    }
    let countBefore = getCount(beforeRes);

    // send ping
    let pingRes = http.post(`${BASE_URL}/ping`, JSON.stringify({lat: uniqueLat, lng: uniqueLng}), {
        tags: { name: 'POST /ping' },
        timeout: '5s',
    })
    if (pingRes.status !== 201) {
        // write failed during churn, skip this check
        return;
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

    // if both requests succeeded, verify correctness
    let valid = check(countAfter, {
        'correctness: count increased by 1': () => countAfter === countBefore + 1
    });
    correctnessSuccess.add(valid);
}

export function handleSummary(data) {
    return {
        'outputs/worker_churn_summary.json': JSON.stringify(data, null, 2),
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