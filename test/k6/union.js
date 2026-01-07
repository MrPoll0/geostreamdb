import http from 'k6/http'
import { check, sleep } from 'k6'

export const options = {
    scenarios: {
        test: {
            executor: 'constant-vus',
            vus: 3,
            duration: '1m',
        }
    },
    thresholds: {
        checks: ['rate==1.0'],  // all checks must pass
    }
}

const BASE_URL = __ENV.ENTRYPOINT_URL || 'http://localhost:8080'
const REFLECT_DELAY = parseFloat(__ENV.REFLECT_DELAY) || 0.25
const MAX_PRECISION = parseInt(__ENV.MAX_PRECISION) || 8
const BBOX_R = 0.002
const BBOX_SEP = 0.006 // > 2*BBOX_R -> disjoint, but still close (keeps union rectangle small)

const clamp = (x, lo, hi) => Math.min(Math.max(x, lo), hi)

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

export default function() {
    // A bbox
    const uniqueLat = Math.random() * 160 - 80 // avoid issues at edges
    const uniqueLng = Math.random() * 340 - 170
    let bbox = {
        minLat: clamp(uniqueLat - BBOX_R, -90, 90),
        maxLat: clamp(uniqueLat + BBOX_R, -90, 90),
        minLng: clamp(uniqueLng - BBOX_R, -180, 180),
        maxLng: clamp(uniqueLng + BBOX_R, -180, 180),
    }

    // B disjoint bbox, close enough that the union rectangle is small
    const uniqueLat2 = clamp(uniqueLat + BBOX_SEP, -80, 80)
    const uniqueLng2 = uniqueLng
    let bbox2 = {
        minLat: clamp(uniqueLat2 - BBOX_R, -90, 90),
        maxLat: clamp(uniqueLat2 + BBOX_R, -90, 90),
        minLng: clamp(uniqueLng2 - BBOX_R, -180, 180),
        maxLng: clamp(uniqueLng2 + BBOX_R, -180, 180),
    }
    
    // stage 0: send N and N2 pings to two disjoint bboxes
    const N = Math.floor(Math.random() * 10) + 1
    for(let i = 0; i < N; i++) {
        let res = http.post(`${BASE_URL}/ping`, JSON.stringify({lat: uniqueLat, lng: uniqueLng}))
        check(res, {
            [`Ping ${i}: status is 201`]: () => res.status === 201
        })
    }

    const N2 = Math.floor(Math.random() * 10) + 1
    for(let i = 0; i < N2; i++) {
        let res = http.post(`${BASE_URL}/ping`, JSON.stringify({lat: uniqueLat2, lng: uniqueLng2}))
        check(res, {
            [`Ping ${i}: status is 201`]: () => res.status === 201
        })
    }

    sleep(REFLECT_DELAY)

    // stage 1: verify each bbox independently
    let resA = http.get(`${BASE_URL}/pingArea?precision=${MAX_PRECISION}&minLat=${bbox.minLat}&maxLat=${bbox.maxLat}&minLng=${bbox.minLng}&maxLng=${bbox.maxLng}`)
    check(resA, { [`A: status is 200`]: () => resA.status === 200 })
    let countA = getCount(resA)
    check(countA, { [`A: count is N`]: () => countA === N })

    let resB = http.get(`${BASE_URL}/pingArea?precision=${MAX_PRECISION}&minLat=${bbox2.minLat}&maxLat=${bbox2.maxLat}&minLng=${bbox2.minLng}&maxLng=${bbox2.maxLng}`)
    check(resB, { [`B: status is 200`]: () => resB.status === 200 })
    let countB = getCount(resB)
    check(countB, { [`B: count is N2`]: () => countB === N2 })

    // stage 2: verify count(bbox_A U bbox_B) = count(A) + count(B)
    const unionBbox = {
        minLat: Math.min(bbox.minLat, bbox2.minLat),
        maxLat: Math.max(bbox.maxLat, bbox2.maxLat),
        minLng: Math.min(bbox.minLng, bbox2.minLng),
        maxLng: Math.max(bbox.maxLng, bbox2.maxLng),
    }
    let resU = http.get(`${BASE_URL}/pingArea?precision=${MAX_PRECISION}&minLat=${unionBbox.minLat}&maxLat=${unionBbox.maxLat}&minLng=${unionBbox.minLng}&maxLng=${unionBbox.maxLng}`)
    check(resU, { [`U: status is 200`]: () => resU.status === 200 })
    let countU = getCount(resU)
    check(countU, { [`U: count is N + N2`]: () => countU === N + N2 })
    if (countU !== N + N2) {
        console.log(`U: ${countU} expected ${N + N2} (A=${countA}, B=${countB})`)
    }


    // almost impossible for bboxes to overlap, so no real need to wait for TTL to expire
}

export function handleSummary(data) {
    return {
        'stdout': JSON.stringify(data.metrics, null, 2),
        'union_summary.json': JSON.stringify(data, null, 2),
    }
}