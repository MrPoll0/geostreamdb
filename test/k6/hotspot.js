import http from 'k6/http';
import { check, sleep } from 'k6';
import { Rate, Counter } from 'k6/metrics';

const DURATION = '5m';
const HOTSPOT_RATIO = parseFloat(__ENV.HOTSPOT_RATIO) || 0.9; // 90% of writes to hotspot

export const options = {
    scenarios: {
        load: {
            executor: 'constant-arrival-rate',
            duration: DURATION,
            rate: 800,
            preAllocatedVUs: 50,
            timeUnit: '1s',
            maxVUs: 200,
            exec: 'load',
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
        http_req_duration: ['p(95)<500', 'p(99)<1000'],
        ping_success: ['rate>0.95'],
        correctness_success: ['rate>0.99'],
        aggregation_success: ['rate>0.99'],
        http_req_failed: ['rate<0.05'],
    }
}

const BASE_URL = __ENV.ENTRYPOINT_URL || 'http://localhost:8080'
const REFLECT_DELAY = parseFloat(__ENV.REFLECT_DELAY) || 0.5
const MAX_PRECISION = parseInt(__ENV.MAX_PRECISION) || 8

// metrics
const pingSuccess = new Rate('ping_success');
const correctnessSuccess = new Rate('correctness_success');
const aggregationSuccess = new Rate('aggregation_success');
const hotspotWrites = new Counter('hotspot_writes');
const globalWrites = new Counter('global_writes');

// hotspot region: Vigo, Spain
// this maps to a small number of geohash prefixes (high contention)
const HOTSPOT = {
    lat: 42.232,
    lng: -8.726,
    jitter: 0.005,
}

function getHotspotCoords() {
    return {
        lat: HOTSPOT.lat + (Math.random() - 0.5) * HOTSPOT.jitter,
        lng: HOTSPOT.lng + (Math.random() - 0.5) * HOTSPOT.jitter,
    }
}

function getGlobalCoords() {
    return {
        lat: Math.random() * 160 - 80,  // -80 to 80
        lng: Math.random() * 340 - 170, // -170 to 170
    }
}

// main load scenario: 90% hotspot, 10% global
export function load() {
    let coords;
    if (Math.random() < HOTSPOT_RATIO) {
        // hotspot
        coords = getHotspotCoords();
        hotspotWrites.add(1);
    } else {
        // global
        coords = getGlobalCoords();
        globalWrites.add(1);
    }

    let res = http.post(`${BASE_URL}/ping`, JSON.stringify({lat: coords.lat, lng: coords.lng}), {
        tags: { name: 'POST /ping' }
    })
    let success = check(res, { 'load: ping status 201': () => res.status === 201 })
    pingSuccess.add(success);
}

// correctness scenario: write-then-read in ISOLATED region (not hotspot)
// uses sparse arctic region to avoid interference from hotspot load
export function correctness() {
    // isolated region far from hotspot
    let uniqueLat = 85 + Math.random() * 4  // 85-89 (arctic)
    let uniqueLng = 170 + Math.random() * 8 // far from hotspot
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

    // verify count increased by exactly 1 (isolated region, no interference)
    let valid = check(countAfter, {
        'correctness: count increased by 1': () => countAfter === countBefore + 1
    });
    correctnessSuccess.add(valid);
}

// aggregation scenario: verify count consistency across precision levels
// uses ISOLATED region to avoid race conditions from hotspot load
export function aggregation() {
    // isolated region far from hotspot (antarctic)
    let uniqueLat = -85 + Math.random() * 4  // -85 to -81
    let uniqueLng = 160 + Math.random() * 10
    let bbox = {
        minLat: uniqueLat - 0.005,
        maxLat: uniqueLat + 0.005,
        minLng: uniqueLng - 0.005,
        maxLng: uniqueLng + 0.005,
    }

    // send N pings to isolated location
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
        'outputs/hotspot_summary.json': JSON.stringify(data, null, 2),
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