import http from 'k6/http';
import { check, sleep } from 'k6';

export const options = {
    scenarios: {
        test: {
            executor: 'shared-iterations',
            vus: 1,
            iterations: 25,
            maxDuration: '10m',
        }
    },
    thresholds: {
        checks: ['rate==1.0'],  // all checks must pass
    }
}

const BASE_URL = __ENV.ENTRYPOINT_URL || 'http://localhost:8080'
const MAX_PRECISION = parseInt(__ENV.MAX_PRECISION) || 8
const GEOHASH_BASE32 = "0123456789bcdefghjkmnpqrstuvwxyz";
const TTL = parseInt(__ENV.PING_TTL) || 10;
const TTL_MARGIN = parseInt(__ENV.TTL_MARGIN) || 2;

export default function() {
    // stage 0: choose random geohash and precision
    const precision = Math.floor(Math.random() * MAX_PRECISION) + 1;
    let geohash = '';
    for (let i = 0; i < precision; i++) {
        geohash += GEOHASH_BASE32[Math.floor(Math.random() * GEOHASH_BASE32.length)];
    }

    // stage 1: decode geohash to bbox
    const [bbox, success] = geohashDecodeBbox(geohash);
    if (!success) {
        console.error("Failed to decode geohash");
        return;
    }

    // stage 2: send pings to edges and corners and verify the count is correct
    // (HALF-OPEN BBOX)
    let counted = 0

    // EDGES (centered)
    // bottom edge: should include it
    sendPing(bbox.minLat, (bbox.minLng + bbox.maxLng) / 2, 'Bottom edge');
    counted = sendReadAndCount(counted + 1, precision, bbox.minLat, bbox.maxLat, bbox.minLng, bbox.maxLng, 'Bottom edge');

    // left edge: should include it
    sendPing((bbox.minLat + bbox.maxLat) / 2, bbox.minLng, 'Left edge');
    counted = sendReadAndCount(counted + 1, precision, bbox.minLat, bbox.maxLat, bbox.minLng, bbox.maxLng, 'Left edge');

    // right edge: should NOT include it
    sendPing((bbox.minLat + bbox.maxLat) / 2, bbox.maxLng, 'Right edge');
    counted = sendReadAndCount(counted, precision, bbox.minLat, bbox.maxLat, bbox.minLng, bbox.maxLng, 'Right edge');

    // top edge: should NOT include it
    sendPing(bbox.maxLat, (bbox.minLng + bbox.maxLng) / 2, 'Top edge');
    counted = sendReadAndCount(counted, precision, bbox.minLat, bbox.maxLat, bbox.minLng, bbox.maxLng, 'Top edge');


    // CORNERS
    // top-left corner: should NOT include it
    sendPing(bbox.maxLat, bbox.minLng, 'Top-left corner');
    counted = sendReadAndCount(counted, precision, bbox.minLat, bbox.maxLat, bbox.minLng, bbox.maxLng, 'Top-left corner');

    // top-right corner: should NOT include it
    sendPing(bbox.maxLat, bbox.maxLng, 'Top-right corner');
    counted = sendReadAndCount(counted, precision, bbox.minLat, bbox.maxLat, bbox.minLng, bbox.maxLng, 'Top-right corner');

    // bottom-left corner: should include it
    sendPing(bbox.minLat, bbox.minLng, 'Bottom-left corner');
    counted = sendReadAndCount(counted + 1, precision, bbox.minLat, bbox.maxLat, bbox.minLng, bbox.maxLng, 'Bottom-left corner');

    // bottom-right corner: should NOT include it
    sendPing(bbox.minLat, bbox.maxLng, 'Bottom-right corner');
    counted = sendReadAndCount(counted, precision, bbox.minLat, bbox.maxLat, bbox.minLng, bbox.maxLng, 'Bottom-right corner');

    // stage 3: wait for pings to expire
    // since we are sending pings of a variety of precisions, we need to wait for TTL + TTL_MARGIN to ensure no test case overlaps with another
    sleep(TTL + TTL_MARGIN);
}

export function handleSummary(data) {
    return {
        'stdout': JSON.stringify(data.metrics, null, 2),
        'outputs/boundary_summary.json': JSON.stringify(data, null, 2),
    }
}

const sendPing = (lat, lng, comment = 'Unknown') => {
    let res = http.post(`${BASE_URL}/ping`, JSON.stringify({lat: lat, lng: lng}));
    check(res, { [`${comment}: Ping status is 201`]: () => res.status === 201 });
    return res.status === 201;
}

const sendReadAndCount = (N, precision, minLat, maxLat, minLng, maxLng, comment = 'Unknown') => {
    let res = http.get(`${BASE_URL}/pingArea?precision=${precision}&minLat=${minLat}&maxLat=${maxLat}&minLng=${minLng}&maxLng=${maxLng}`);
    check(res, { [`${comment}: PingArea status is 200`]: () => res.status === 200 });
    let count = getCount(res);
    check(count, { [`${comment}: PingArea count is expected`]: () => count === N });
    if (count !== N) {
        console.log(`${comment}: ${count} expected ${N}`)
    }
    return count;
}

const getCount = (res) => {
    try {
        const data = JSON.parse(res.body)
        let total = 0
        for (const key in data) {
            if (data[key] && typeof data[key].Count === 'number') {
                total += data[key].Count
            }
        }
        return total
    } catch {
        console.error(`Error parsing response: ${res.body}`)
        return -1
    }
}

function geohashDecodeBbox(gh) {
    if (!gh || typeof gh !== "string" || gh.length === 0) {
        return [null, false];
    }

    const geohashBase32 = "0123456789bcdefghjkmnpqrstuvwxyz";
    const charmap = new Array(256).fill(0xFF);
    for (let i = 0; i < geohashBase32.length; i++) {
        charmap[geohashBase32.charCodeAt(i)] = i;
    }

    let minLat = -90.0, maxLat = 90.0;
    let minLng = -180.0, maxLng = 180.0;
    let isLng = true;

    for (let i = 0; i < gh.length; i++) {
        let c = gh.charCodeAt(i);
        // normalize ASCII uppercase -> lowercase
        if (c >= 65 && c <= 90) { // 'A'..'Z'
            c = c + 32;
        }
        let v = charmap[c];
        if (v === 0xFF) {
            return [null, false];
        }
        for (let bit = 4; bit >= 0; bit--) {
            let mask = 1 << bit;
            if (isLng) {
                let mid = (minLng + maxLng) / 2.0;
                if ((v & mask) !== 0) {
                    minLng = mid;
                } else {
                    maxLng = mid;
                }
            } else {
                let mid = (minLat + maxLat) / 2.0;
                if ((v & mask) !== 0) {
                    minLat = mid;
                } else {
                    maxLat = mid;
                }
            }
            isLng = !isLng;
        }
    }
    // return bbox and success=true
    return [{
        minLat: minLat,
        maxLat: maxLat,
        minLng: minLng,
        maxLng: maxLng
    }, true];
}