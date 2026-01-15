import http from 'k6/http'
import { check, sleep } from 'k6'

export const options = {
    scenarios: {
        test: {
            executor: 'constant-vus',
            duration: '1m',
            vus: 3,
        }
    },
    thresholds: {
        checks: ['rate==1.0'],  // all checks must pass
    }
}

const BASE_URL = __ENV.ENTRYPOINT_URL || 'http://localhost:8080'
const REFLECT_DELAY = parseFloat(__ENV.REFLECT_DELAY) || 0.25
const MAX_PRECISION = parseInt(__ENV.MAX_PRECISION) || 8

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
    const uniqueLat = Math.random() * 160 - 80 // avoid issues at edges
    const uniqueLng = Math.random() * 340 - 170
    let bbox = {
        minLat: uniqueLat - 0.005,
        maxLat: uniqueLat + 0.005,
        minLng: uniqueLng - 0.005,
        maxLng: uniqueLng + 0.005,
    }

    // stage 0: send N pingsto a unique location
    const N = Math.floor(Math.random() * 10) + 1
    // console.log(`Sending ${N} pings to ${uniqueLat}, ${uniqueLng}`)
    for(let i = 0; i < N; i++) {
        let res = http.post(`${BASE_URL}/ping`, JSON.stringify({lat: uniqueLat, lng: uniqueLng}))
        check(res, { [`Ping ${i}: status is 201`]: () => res.status === 201 })
    }

    sleep(REFLECT_DELAY)

    // stage 1: for every precision possible and same bbox, verify the count is N
    for(let p = MAX_PRECISION; p >= 1; p--) {
        /*let c = 0.002 ** (1 / (MAX_PRECISION - p + 1))

        let bbox = {
            minLat: Math.min(Math.max(uniqueLat - c, -90), 90),
            maxLat: Math.min(Math.max(uniqueLat + c, -90), 90),
            minLng: Math.min(Math.max(uniqueLng - c, -180), 180),
            maxLng: Math.min(Math.max(uniqueLng + c, -180), 180),
        }*/

        let res = http.get(`${BASE_URL}/pingArea?precision=${p}&minLat=${bbox.minLat}&maxLat=${bbox.maxLat}&minLng=${bbox.minLng}&maxLng=${bbox.maxLng}`)
        check(res, { [`Precision ${p}: status is 200`]: () => res.status === 200 })
        
        let count = getCount(res)
        if (count != N){
            console.log(`Precision ${p}: ${count} expected ${N}`)
        }
        check(count, { [`Precision ${p}: count is N`]: () => count === N })
    }
}

export function handleSummary(data) {
    return {
        'outputs/aggregation_summary.json': JSON.stringify(data, null, 2),
    }
}