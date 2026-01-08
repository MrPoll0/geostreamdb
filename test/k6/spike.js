import http from 'k6/http';
import { check, sleep } from 'k6';
import { Rate, Trend, Counter, Gauge } from 'k6/metrics';

// 1. Warm up at low load (10 VUs)
// 2. Spike to 300 VUs in 5 seconds
// 3. Hold spike for 30 seconds
// 4. Drop back to 10 VUs
// 5. Recovery period

export const options = {
    scenarios: {
        // POST /ping
        write_spike: {
            executor: 'ramping-vus',
            startVUs: 0,
            stages: [
                { duration: '10s', target: 10 },    // warm up
                { duration: '5s', target: 100 },    // spike! (5s ramp)
                { duration: '30s', target: 100 },   // hold spike
                { duration: '5s', target: 10 },     // drop
                { duration: '20s', target: 10 },    // recovery
                { duration: '5s', target: 0 },      // ramp down
            ],
            exec: 'writeSpike',
        },
        // GET /pingArea
        read_spike: {
            executor: 'ramping-vus',
            startVUs: 0,
            stages: [
                { duration: '10s', target: 5 },     // warm up
                { duration: '5s', target: 50 },     // spike! (5s ramp)
                { duration: '30s', target: 50 },    // hold spike
                { duration: '5s', target: 5 },      // drop
                { duration: '20s', target: 5 },     // recovery
                { duration: '5s', target: 0 },      // ramp down
            ],
            exec: 'readSpike',
        },
        // monitor during spike
        monitor: {
            executor: 'constant-arrival-rate',
            duration: '75s',
            rate: 1,
            preAllocatedVUs: 1,
            timeUnit: '1s',
            maxVUs: 2,
            exec: 'monitor',
        },
    },
    thresholds: {
        // writes: allow some degradation during spike
        write_success: ['rate>0.90'],               // 90% success during spike
        write_duration: ['p(95)<3000'],             // p95 under 3s during spike
        
        // reads: may be slower due to load
        read_success: ['rate>0.85'],                // 85% success (reads are heavier)
        read_duration: ['p(95)<5000'],              // p95 under 5s during spike
        
        // overall
        http_req_failed: ['rate<0.15'],             // <15% errors during spike
        workers_available: ['rate>0.99'],           // workers should remain visible
    }
}

const BASE_URL = __ENV.ENTRYPOINT_URL || 'http://localhost:8080'

// metrics
const writeSuccess = new Rate('write_success');
const writeDuration = new Trend('write_duration');
const readSuccess = new Rate('read_success');
const readDuration = new Trend('read_duration');
const workerCount = new Gauge('worker_count');
const workersAvailable = new Rate('workers_available');
const spikePhaseGauge = new Gauge('spike_phase');
const concurrentVUs = new Gauge('concurrent_vus');

// geographic hotspots for realistic clustering
const HOTSPOTS = [
    { name: 'Vigo', lat: 42.2317, lng: -8.7263, radius: 0.05 },
    { name: 'NYC', lat: 40.7128, lng: -74.0060, radius: 0.05 },
    { name: 'LA', lat: 34.0522, lng: -118.2437, radius: 0.05 },
    { name: 'London', lat: 51.5074, lng: -0.1278, radius: 0.05 },
    { name: 'Tokyo', lat: 35.6762, lng: 139.6503, radius: 0.05 }
];

function getRandomLocation() {
    const hotspot = HOTSPOTS[Math.floor(Math.random() * HOTSPOTS.length)];
    return {
        lat: hotspot.lat + (Math.random() - 0.5) * hotspot.radius * 2,
        lng: hotspot.lng + (Math.random() - 0.5) * hotspot.radius * 2,
    };
}

function getBbox(hotspot) {
    return {
        minLat: hotspot.lat - hotspot.radius,
        maxLat: hotspot.lat + hotspot.radius,
        minLng: hotspot.lng - hotspot.radius,
        maxLng: hotspot.lng + hotspot.radius,
    };
}

// POST /ping
export function writeSpike() {
    const loc = getRandomLocation();
    
    const start = Date.now();
    const res = http.post(`${BASE_URL}/ping`, JSON.stringify({
        lat: loc.lat,
        lng: loc.lng,
    }), {
        tags: { name: 'POST /ping' },
        timeout: '10s',
    });
    const duration = Date.now() - start;
    
    const success = res.status === 201;
    writeSuccess.add(success);
    writeDuration.add(duration);
    
    check(res, {
        'write: status 201': () => res.status === 201,
        'write: duration under 3s': () => duration < 3000,
    });
    
    sleep(0.05);  // 50ms between writes
}

// GET /pingArea
export function readSpike() {
    const hotspot = HOTSPOTS[Math.floor(Math.random() * HOTSPOTS.length)];
    const bbox = getBbox(hotspot);
    
    const start = Date.now();
    const res = http.get(
        `${BASE_URL}/pingArea?precision=5&minLat=${bbox.minLat}&maxLat=${bbox.maxLat}&minLng=${bbox.minLng}&maxLng=${bbox.maxLng}`,
        {
            tags: { name: 'GET /pingArea' },
            timeout: '15s',
        }
    );
    const duration = Date.now() - start;
    
    const success = res.status === 200;
    readSuccess.add(success);
    readDuration.add(duration);
    
    check(res, {
        'read: status 200': () => res.status === 200,
        'read: valid JSON': () => {
            try {
                JSON.parse(res.body);
                return true;
            } catch {
                return false;
            }
        },
        'read: duration under 5s': () => duration < 5000,
    });
    
    sleep(0.1);  // 100ms between reads
}

// track system health during spike
export function monitor() {
    const res = http.get(`${BASE_URL}/metrics`, {
        tags: { name: 'GET /metrics' },
        timeout: '5s',
    });
    
    if (res.status !== 200) {
        console.log(`monitor: metrics unavailable (status ${res.status})`);
        workersAvailable.add(false);
        return;
    }
    
    // parse worker count
    const lines = res.body.split('\n');
    let count = 0;
    for (const line of lines) {
        if (line.startsWith('gateway_worker_nodes_total ')) {
            const parts = line.split(' ');
            if (parts.length >= 2) {
                count = parseInt(parts[1]) || 0;
                break;
            }
        }
    }
    
    workerCount.add(count);
    workersAvailable.add(count > 0);
    
    // track VU count for analysis
    concurrentVUs.add(__VU);
    
    console.log(`monitor: ${count} workers, load phase active`);
}

export function handleSummary(data) {
    const metrics = data.metrics;
    
    const summary = {
        write_success_rate: metrics.write_success ? metrics.write_success.values.rate : null,
        write_p95_duration: metrics.write_duration ? metrics.write_duration.values['p(95)'] : null,
        read_success_rate: metrics.read_success ? metrics.read_success.values.rate : null,
        read_p95_duration: metrics.read_duration ? metrics.read_duration.values['p(95)'] : null,
        workers_available: metrics.workers_available ? metrics.workers_available.values.rate : null,
        http_req_failed: metrics.http_req_failed ? metrics.http_req_failed.values.rate : null,
    };
    
    console.log('\n========== SPIKE TEST SUMMARY ==========');
    console.log(`Write success rate: ${(summary.write_success_rate * 100).toFixed(1)}%`);
    console.log(`Write p95 duration: ${summary.write_p95_duration?.toFixed(0)}ms`);
    console.log(`Read success rate: ${(summary.read_success_rate * 100).toFixed(1)}%`);
    console.log(`Read p95 duration: ${summary.read_p95_duration?.toFixed(0)}ms`);
    console.log(`Workers available: ${(summary.workers_available * 100).toFixed(1)}%`);
    console.log(`HTTP error rate: ${(summary.http_req_failed * 100).toFixed(1)}%`);
    console.log('==========================================\n');
    
    return {
        'stdout': JSON.stringify(summary, null, 2),
        'outputs/spike_summary.json': JSON.stringify(data, null, 2),
    };
}

