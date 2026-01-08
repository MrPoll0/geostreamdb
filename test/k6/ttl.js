import http from 'k6/http';
import { sleep, check } from 'k6';

export const options = {
    scenarios: {
        test: {
            executor: 'shared-iterations',
            vus: 1,
            iterations: 10,
            maxDuration: '3m'
        }
    },
    thresholds: {
        checks: ['rate==1.0'],  // all checks must pass
    },
}

const BASE_URL = __ENV.ENTRYPOINT_URL || 'http://localhost:8080'
const PRECISION = parseInt(__ENV.MAX_PRECISION) || 8
const PING_TTL = parseInt(__ENV.PING_TTL) || 10
const TTL_MARGIN = parseInt(__ENV.TTL_MARGIN) || 2 // current implementation has ~1s of ambiguity around the TTL boundary + time between gateway and worker + scheduling jitter. this is a hard bound
const REFLECT_DELAY = parseFloat(__ENV.REFLECT_DELAY) || 0.25

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

export default function () {
    const uniqueLat = Math.random() * 160 - 80 // avoid issues at edges
    const uniqueLng = Math.random() * 340 - 170
    const bbox = {
        minLat: uniqueLat - 0.005,
        maxLat: uniqueLat + 0.005,
        minLng: uniqueLng - 0.005,
        maxLng: uniqueLng + 0.005,
    }

    // stage 0: send ping
    let res = http.post(`${BASE_URL}/ping`, JSON.stringify({lat: uniqueLat, lng: uniqueLng}), {
        headers: {
            'Content-Type': 'application/json'
        }
    })
    check(res, { 'Ping status is 201': () => res.status === 201 })

    console.log('Waiting for reflect delay...')
    sleep(REFLECT_DELAY)

    // stage 1: write is reflected soon enough
    const res1 = http.get(`${BASE_URL}/pingArea?precision=${PRECISION}&minLat=${bbox.minLat}&maxLat=${bbox.maxLat}&minLng=${bbox.minLng}&maxLng=${bbox.maxLng}`)
    const count1 = getCount(res1)
    console.log('Count at reflect delay: ', count1)
    check(count1, { 'count1 is 1': () => count1 === 1 })

    console.log('Waiting for TTL - TTL_MARGIN (-reflect)...')
    sleep(PING_TTL - TTL_MARGIN - REFLECT_DELAY)

    // stage 2: ping still alive, inside TTL window
    const res2 = http.get(`${BASE_URL}/pingArea?precision=${PRECISION}&minLat=${bbox.minLat}&maxLat=${bbox.maxLat}&minLng=${bbox.minLng}&maxLng=${bbox.maxLng}`)
    const count2 = getCount(res2)
    console.log('Count after TTL - TTL_MARGIN: ', count2)
    check(count2, { 'count2 is 1': () => count2 === 1 })

    console.log('Waiting for 2 * TTL_MARGIN...')
    sleep(2 * TTL_MARGIN)

    // stage 3: ping expired, outside TTL window
    const res3 = http.get(`${BASE_URL}/pingArea?precision=${PRECISION}&minLat=${bbox.minLat}&maxLat=${bbox.maxLat}&minLng=${bbox.minLng}&maxLng=${bbox.maxLng}`)
    const count3 = getCount(res3)
    console.log('Count after 2 * TTL_MARGIN: ', count3)
    check(count3, { 'count3 is 0': () => count3 === 0 })
}

export function handleSummary(data) {
    return {
        'stdout': JSON.stringify(data.metrics, null, 2),
        'outputs/ttl_summary.json': JSON.stringify(data, null, 2),
    }
}