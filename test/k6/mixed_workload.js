import http from 'k6/http';
import { check, sleep } from 'k6';
import { Rate, Counter, Trend } from 'k6/metrics';

const DURATION = '5m';
const WRITE_RATIO = parseFloat(__ENV.WRITE_RATIO) || 0.8;  // 80% writes, 20% reads

export const options = {
    scenarios: {
        mixed: {
            executor: 'constant-arrival-rate',
            duration: DURATION,
            rate: 500,  // lower than pure write test since reads are heavier
            preAllocatedVUs: 50,
            timeUnit: '1s',
            maxVUs: 200,
            exec: 'mixed',
        },
        correctness: {
            executor: 'constant-arrival-rate',
            duration: DURATION,
            rate: 1,
            preAllocatedVUs: 2,
            timeUnit: '1s',
            maxVUs: 5,
            exec: 'correctness',
        },
        aggregation: {
            executor: 'constant-arrival-rate',
            duration: DURATION,
            rate: 1,
            preAllocatedVUs: 2,
            timeUnit: '1s',
            maxVUs: 5,
            exec: 'aggregation',
        }
    },
    thresholds: {
        http_req_duration: ['p(95)<750', 'p(99)<1500'],  // relaxed for mixed workload
        write_success: ['rate>0.95'],
        read_success: ['rate>0.95'],
        correctness_success: ['rate>0.99'],
        aggregation_success: ['rate>0.99'],
        http_req_failed: ['rate<0.05'],
        write_latency: ['p(95)<600'],   // writes impacted by concurrent reads (especially on single machine)
        read_latency: ['p(95)<800'],    // reads are heavyweight (variable precision)
    }
}

const BASE_URL = __ENV.ENTRYPOINT_URL || 'http://localhost:8080'
const REFLECT_DELAY = parseFloat(__ENV.REFLECT_DELAY) || 0.5
const MAX_PRECISION = parseInt(__ENV.MAX_PRECISION) || 8

// metrics
const writeSuccess = new Rate('write_success');
const readSuccess = new Rate('read_success');
const correctnessSuccess = new Rate('correctness_success');
const aggregationSuccess = new Rate('aggregation_success');
const writeCount = new Counter('write_count');
const readCount = new Counter('read_count');
const writeLatency = new Trend('write_latency');
const readLatency = new Trend('read_latency');

// mixed scenario: 80% writes, 20% reads
export function mixed() {
    if (Math.random() < WRITE_RATIO) {
        // WRITE: send ping to random location
        let lat = Math.random() * 160 - 80
        let lng = Math.random() * 340 - 170
        
        let start = Date.now();
        let res = http.post(`${BASE_URL}/ping`, JSON.stringify({lat: lat, lng: lng}), {
            tags: { name: 'POST /ping' }  // group all writes under one metric
        })
        writeLatency.add(Date.now() - start);
        
        let success = check(res, { 'write: status 201': () => res.status === 201 })
        writeSuccess.add(success);
        writeCount.add(1);
    } else {
        // READ: query pingArea for random region
        let lat = Math.random() * 160 - 80
        let lng = Math.random() * 340 - 170
        let precision = Math.floor(Math.random() * MAX_PRECISION) + 1
        let bbox = getBbox(lat, lng, precision);
        
        let start = Date.now();
        let res = http.get(`${BASE_URL}/pingArea?precision=${precision}&minLat=${bbox.minLat}&maxLat=${bbox.maxLat}&minLng=${bbox.minLng}&maxLng=${bbox.maxLng}`, {
            tags: { name: 'GET /pingArea' }  // group all reads under one metric
        })
        readLatency.add(Date.now() - start);
        
        let success = check(res, { 'read: status 200': () => res.status === 200 })
        readSuccess.add(success);
        readCount.add(1);
    }
}

// correctness scenario: write then verify read
export function correctness() {
    // isolated region
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
        tags: { name: 'GET /pingArea' }
    })
    if (beforeRes.status !== 200) {
        correctnessSuccess.add(false);
        return;
    }
    let countBefore = getCount(beforeRes);

    // send ping
    let pingRes = http.post(`${BASE_URL}/ping`, JSON.stringify({lat: uniqueLat, lng: uniqueLng}), {
        tags: { name: 'POST /ping' }
    })
    if (pingRes.status !== 201) {
        correctnessSuccess.add(false);
        return;
    }

    sleep(REFLECT_DELAY);

    // get count after
    let afterRes = http.get(`${BASE_URL}/pingArea?precision=8&minLat=${bbox.minLat}&maxLat=${bbox.maxLat}&minLng=${bbox.minLng}&maxLng=${bbox.maxLng}`, {
        tags: { name: 'GET /pingArea' }
    })
    if (afterRes.status !== 200) {
        correctnessSuccess.add(false);
        return;
    }
    let countAfter = getCount(afterRes);

    let valid = check(countAfter, {
        'correctness: count increased by 1': () => countAfter === countBefore + 1
    });
    correctnessSuccess.add(valid);
}

// aggregation scenario: verify count consistency across precision levels
export function aggregation() {
    // isolated region
    let uniqueLat = -85 + Math.random() * 4
    let uniqueLng = 160 + Math.random() * 10
    let bbox = {
        minLat: uniqueLat - 0.005,
        maxLat: uniqueLat + 0.005,
        minLng: uniqueLng - 0.005,
        maxLng: uniqueLng + 0.005,
    }

    // send N pings
    const N = Math.floor(Math.random() * 5) + 1
    for (let i = 0; i < N; i++) {
        let res = http.post(`${BASE_URL}/ping`, JSON.stringify({lat: uniqueLat, lng: uniqueLng}), {
            tags: { name: 'POST /ping' }
        })
        if (res.status !== 201) {
            aggregationSuccess.add(false);
            return;
        }
    }

    sleep(REFLECT_DELAY);

    // verify count is N at all precision levels
    let allMatch = true;
    for (let p = MAX_PRECISION; p >= 1; p--) {
        let res = http.get(`${BASE_URL}/pingArea?precision=${p}&minLat=${bbox.minLat}&maxLat=${bbox.maxLat}&minLng=${bbox.minLng}&maxLng=${bbox.maxLng}`, {
            tags: { name: 'GET /pingArea' }
        })
        if (res.status !== 200) {
            allMatch = false;
            break;
        }
        let count = getCount(res);
        if (count !== N) {
            allMatch = false;
            break;
        }
    }

    let valid = check(allMatch, {
        'aggregation: count matches at all precisions': () => allMatch === true
    });
    aggregationSuccess.add(valid);
}

export function handleSummary(data) {
    return {
        'stdout': JSON.stringify(data.metrics, null, 2),
        'mixed_workload_summary.json': JSON.stringify(data, null, 2),
    }
}

function getBbox(lat, lng, precision) {
    // get bbox for a given center and precision
    // approximate bbox size based on precision
    let sizes = [45, 11, 2.8, 0.7, 0.17, 0.044, 0.011, 0.0027];  // degrees per cell
    let size = sizes[precision - 1] || 0.01;
    return {
        minLat: Math.max(lat - size, -90),
        maxLat: Math.min(lat + size, 90),
        minLng: Math.max(lng - size, -180),
        maxLng: Math.min(lng + size, 180),
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
