// Tests the constraints of the /ping and /pingArea endpoints (boundary conditions, invalid values, etc.)

import http from 'k6/http'
import { check } from 'k6'

export const options = {
    scenarios: {
        test: {
            executor: 'shared-iterations',
            vus: 1,
            iterations: 1,
            maxDuration: '5m',
        }
    },
    thresholds: {
        checks: ['rate==1.0'],  // all checks must pass
    }
}

const BASE_URL = __ENV.ENTRYPOINT_URL || 'http://localhost:8080'

export default function() {
    // =========================================================================
    // /ping ENDPOINT TESTS
    // =========================================================================

    // ===== VALID EDGE CASES (should return 201) =====
    // latitude extremes
    checkPing(90, 0, 201, 'lat=90 (north pole)')
    checkPing(-90, 0, 201, 'lat=-90 (south pole)')
    checkPing(89.999999, 0, 201, 'lat near +90')
    checkPing(-89.999999, 0, 201, 'lat near -90')

    // longitude extremes
    checkPing(0, 180, 201, 'lng=180')
    checkPing(0, -180, 201, 'lng=-180')
    checkPing(0, 179.999999, 201, 'lng near +180')
    checkPing(0, -179.999999, 201, 'lng near -180')

    // corner cases (all 4 corners of valid range)
    checkPing(90, 180, 201, 'corner: +90, +180')
    checkPing(90, -180, 201, 'corner: +90, -180')
    checkPing(-90, 180, 201, 'corner: -90, +180')
    checkPing(-90, -180, 201, 'corner: -90, -180')

    // zero crossing
    checkPing(0, 0, 201, 'origin: 0, 0')

    // ===== INVALID: OUT OF BOUNDS (should return 400) =====
    // latitude out of bounds
    checkPing(90.0001, 0, 400, 'lat > 90')
    checkPing(-90.0001, 0, 400, 'lat < -90')
    checkPing(91, 0, 400, 'lat = 91')
    checkPing(-91, 0, 400, 'lat = -91')
    checkPing(1000, 0, 400, 'lat = 1000')
    checkPing(-1000, 0, 400, 'lat = -1000')

    // longitude out of bounds
    checkPing(0, 180.0001, 400, 'lng > 180')
    checkPing(0, -180.0001, 400, 'lng < -180')
    checkPing(0, 181, 400, 'lng = 181')
    checkPing(0, -181, 400, 'lng = -181')
    checkPing(0, 1000, 400, 'lng = 1000')
    checkPing(0, -1000, 400, 'lng = -1000')

    // both out of bounds
    checkPing(100, 200, 400, 'both lat and lng out of bounds')

    // ===== INVALID: SPECIAL VALUES (should return 400) =====
    // infinity
    checkPing(Infinity, 0, 400, 'lat = Infinity')
    checkPing(-Infinity, 0, 400, 'lat = -Infinity')
    checkPing(0, Infinity, 400, 'lng = Infinity')
    checkPing(0, -Infinity, 400, 'lng = -Infinity')

    // NaN (serializes to null in JSON)
    checkPing(NaN, 0, 400, 'lat = NaN')
    checkPing(0, NaN, 400, 'lng = NaN')
    checkPing(NaN, NaN, 400, 'both NaN')

    // ===== INVALID: WRONG TYPES (should return 400) =====
    // string values
    checkRawPing('{"lat": "45", "lng": 90}', 400, 'lat as string')
    checkRawPing('{"lat": 45, "lng": "90"}', 400, 'lng as string')
    checkRawPing('{"lat": "hello", "lng": 90}', 400, 'lat as non-numeric string')
    checkRawPing('{"lat": 45, "lng": "world"}', 400, 'lng as non-numeric string')
    checkRawPing('{"lat": "", "lng": 90}', 400, 'lat as empty string')
    checkRawPing('{"lat": 45, "lng": ""}', 400, 'lng as empty string')

    // null values
    checkRawPing('{"lat": null, "lng": 90}', 400, 'lat = null')
    checkRawPing('{"lat": 45, "lng": null}', 400, 'lng = null')
    checkRawPing('{"lat": null, "lng": null}', 400, 'both null')

    // boolean values
    checkRawPing('{"lat": true, "lng": 90}', 400, 'lat = true')
    checkRawPing('{"lat": 45, "lng": false}', 400, 'lng = false')

    // array values
    checkRawPing('{"lat": [45], "lng": 90}', 400, 'lat as array')
    checkRawPing('{"lat": 45, "lng": [90]}', 400, 'lng as array')

    // object values
    checkRawPing('{"lat": {"value": 45}, "lng": 90}', 400, 'lat as object')
    checkRawPing('{"lat": 45, "lng": {"value": 90}}', 400, 'lng as object')

    // ===== INVALID: MISSING FIELDS (should return 400) =====
    checkRawPing('{"lng": 90}', 400, 'missing lat')
    checkRawPing('{"lat": 45}', 400, 'missing lng')
    checkRawPing('{}', 400, 'empty object')
    checkRawPing('{"latitude": 45, "longitude": 90}', 400, 'wrong field names')

    // ===== INVALID: MALFORMED JSON (should return 400) =====
    checkRawPing('', 400, 'empty body')
    checkRawPing('not json', 400, 'plain text')
    checkRawPing('{lat: 45, lng: 90}', 400, 'unquoted keys')
    checkRawPing('{"lat": 45, "lng": 90', 400, 'truncated JSON')
    checkRawPing('[45, 90]', 400, 'array instead of object')

    // =========================================================================
    // /pingArea ENDPOINT TESTS
    // =========================================================================

    // ===== VALID EDGE CASES (should return 200) =====
    // full world bbox
    checkPingArea(-90, 90, -180, 180, 1, 200, 'full world bbox')

    // latitude extremes
    checkPingArea(-90, -89, 0, 1, 4, 200, 'minLat=-90')
    checkPingArea(89, 90, 0, 1, 4, 200, 'maxLat=90')
    checkPingArea(-90, 90, 0, 1, 1, 200, 'full lat range')

    // longitude extremes
    checkPingArea(0, 1, -180, -179, 4, 200, 'minLng=-180')
    checkPingArea(0, 1, 179, 180, 4, 200, 'maxLng=180')
    checkPingArea(0, 1, -180, 180, 1, 200, 'full lng range')

    // corner bboxes
    checkPingArea(89, 90, 179, 180, 4, 200, 'corner: NE')
    checkPingArea(89, 90, -180, -179, 4, 200, 'corner: NW')
    checkPingArea(-90, -89, 179, 180, 4, 200, 'corner: SE')
    checkPingArea(-90, -89, -180, -179, 4, 200, 'corner: SW')

    // tiny bbox at origin
    checkPingArea(-0.001, 0.001, -0.001, 0.001, 8, 200, 'tiny bbox at origin')

    // precision extremes (1 to 8)
    checkPingArea(0, 10, 0, 10, 1, 200, 'precision=1')
    checkPingArea(0, 0.01, 0, 0.01, 8, 200, 'precision=8')

    // ===== INVALID: OUT OF BOUNDS (should return 400) =====
    // latitude out of bounds
    checkPingArea(-91, 0, 0, 10, 4, 400, 'minLat < -90')
    checkPingArea(0, 91, 0, 10, 4, 400, 'maxLat > 90')
    checkPingArea(-100, 100, 0, 10, 4, 400, 'both lat out of bounds')

    // longitude out of bounds
    checkPingArea(0, 10, -181, 0, 4, 400, 'minLng < -180')
    checkPingArea(0, 10, 0, 181, 4, 400, 'maxLng > 180')
    checkPingArea(0, 10, -200, 200, 4, 400, 'both lng out of bounds')

    // ===== INVALID: INVERTED BBOX (should return 400) =====
    checkPingArea(10, 0, 0, 10, 4, 400, 'minLat > maxLat')
    checkPingArea(0, 10, 10, 0, 4, 400, 'minLng > maxLng')
    checkPingArea(10, 0, 10, 0, 4, 400, 'both inverted')

    // ===== INVALID: PRECISION OUT OF RANGE (should return 400) =====
    checkPingArea(0, 10, 0, 10, 0, 400, 'precision=0')
    checkPingArea(0, 10, 0, 10, -1, 400, 'precision=-1')
    checkPingArea(0, 10, 0, 10, 9, 400, 'precision=9 (> max)')
    checkPingArea(0, 10, 0, 10, 100, 400, 'precision=100')

    // ===== INVALID: MISSING PARAMETERS (should return 400) =====
    checkRawPingArea('maxLat=10&minLng=0&maxLng=10&precision=4', 400, 'missing minLat')
    checkRawPingArea('minLat=0&minLng=0&maxLng=10&precision=4', 400, 'missing maxLat')
    checkRawPingArea('minLat=0&maxLat=10&maxLng=10&precision=4', 400, 'missing minLng')
    checkRawPingArea('minLat=0&maxLat=10&minLng=0&precision=4', 400, 'missing maxLng')
    checkRawPingArea('minLat=0&maxLat=10&minLng=0&maxLng=10', 400, 'missing precision')
    checkRawPingArea('', 400, 'no parameters')

    // ===== INVALID: WRONG TYPES (should return 400) =====
    // string values for coordinates
    checkRawPingArea('minLat=abc&maxLat=10&minLng=0&maxLng=10&precision=4', 400, 'minLat as string')
    checkRawPingArea('minLat=0&maxLat=abc&minLng=0&maxLng=10&precision=4', 400, 'maxLat as string')
    checkRawPingArea('minLat=0&maxLat=10&minLng=abc&maxLng=10&precision=4', 400, 'minLng as string')
    checkRawPingArea('minLat=0&maxLat=10&minLng=0&maxLng=abc&precision=4', 400, 'maxLng as string')
    checkRawPingArea('minLat=0&maxLat=10&minLng=0&maxLng=10&precision=abc', 400, 'precision as string')

    // empty values
    checkRawPingArea('minLat=&maxLat=10&minLng=0&maxLng=10&precision=4', 400, 'minLat empty')
    checkRawPingArea('minLat=0&maxLat=&minLng=0&maxLng=10&precision=4', 400, 'maxLat empty')
    checkRawPingArea('minLat=0&maxLat=10&minLng=&maxLng=10&precision=4', 400, 'minLng empty')
    checkRawPingArea('minLat=0&maxLat=10&minLng=0&maxLng=&precision=4', 400, 'maxLng empty')
    checkRawPingArea('minLat=0&maxLat=10&minLng=0&maxLng=10&precision=', 400, 'precision empty')

    // special values
    checkRawPingArea('minLat=NaN&maxLat=10&minLng=0&maxLng=10&precision=4', 400, 'minLat=NaN')
    checkRawPingArea('minLat=0&maxLat=Infinity&minLng=0&maxLng=10&precision=4', 400, 'maxLat=Infinity')
    checkRawPingArea('minLat=0&maxLat=10&minLng=-Infinity&maxLng=10&precision=4', 400, 'minLng=-Infinity')
    checkRawPingArea('minLat=0&maxLat=10&minLng=0&maxLng=Infinity&precision=4', 400, 'maxLng=Infinity')
    checkRawPingArea('minLat=0&maxLat=10&minLng=0&maxLng=10&precision=NaN', 400, 'precision=NaN')

    // floating point precision
    checkRawPingArea('minLat=0&maxLat=10&minLng=0&maxLng=10&precision=4.5', 400, 'precision as float')
}

export function handleSummary(data) {
    return {
        'outputs/constraints_summary.json': JSON.stringify(data, null, 2),
    }
}

function checkPing(lat, lng, expectedStatus, label) {
    let res = http.post(`${BASE_URL}/ping`, JSON.stringify({lat: lat, lng: lng}))
    check(res, { [`ping ${label}: status is ${expectedStatus}`]: () => res.status === expectedStatus })
}

function checkRawPing(body, expectedStatus, label) {
    let res = http.post(`${BASE_URL}/ping`, body, {
        headers: { 'Content-Type': 'application/json' }
    })
    check(res, { [`ping ${label}: status is ${expectedStatus}`]: () => res.status === expectedStatus })
}

function checkPingArea(minLat, maxLat, minLng, maxLng, precision, expectedStatus, label) {
    let res = http.get(`${BASE_URL}/pingArea?minLat=${minLat}&maxLat=${maxLat}&minLng=${minLng}&maxLng=${maxLng}&precision=${precision}`)
    check(res, { [`pingArea ${label}: status is ${expectedStatus}`]: () => res.status === expectedStatus })
}

function checkRawPingArea(queryString, expectedStatus, label) {
    let res = http.get(`${BASE_URL}/pingArea?${queryString}`)
    check(res, { [`pingArea ${label}: status is ${expectedStatus}`]: () => res.status === expectedStatus })
}