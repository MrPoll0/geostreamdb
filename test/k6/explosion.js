import http from 'k6/http'
import { check } from 'k6'

export const options = {
    scenarios: {
        test: {
            executor: 'shared-iterations',
            vus: 1,
            iterations: 10,
            maxDuration: '10m',
        }
    }, 
    thresholds: {
        checks: ['rate==1.0'],  // all checks must pass
    }
}

const BASE_URL = __ENV.ENTRYPOINT_URL || 'http://localhost:8080'
const MAX_PINGAREA_GEOHASHES = parseInt(__ENV.MAX_PINGAREA_GEOHASHES) || 5000;
const MAX_PRECISION = parseInt(__ENV.MAX_PRECISION) || 8;

export default function() {
    // random location
    const uniqueLat = Math.random() * 160 - 80 // avoid issues at edges
    const uniqueLng = Math.random() * 340 - 170
    
    for(let precision = 1; precision <= MAX_PRECISION; precision++) {
        // get the width and height of a geohash cell in degrees at the given precision
        // this is used as a step size for the bbox
        let [lonStepDeg, latStepDeg] = geohashCellDimsDegrees(precision);
        
        // choose target cell counts close to MAX_PINGAREA_GEOHASHES to test at the extremes
        let Ns_len = 11
        let Ns = Array.from({ length: Ns_len }, (_, n) => MAX_PINGAREA_GEOHASHES - Math.floor(Ns_len / 2) + n) // e.g. [4995, 4996, 4997, 4998, 4999, 5000, 5001, 5002, 5003, 5004, 5005]
        
        for(let N of Ns) {
            // try different shapes to test the limits
            let shapes = [
                { tag: 'square', wCells: Math.floor(Math.sqrt(N)), hCells: Math.ceil(N / Math.floor(Math.sqrt(N))) }, // square
                { tag: 'wide and tall', wCells: Math.ceil(N / 2), hCells: 2 }, // wide and tall
                { tag: 'tall and wide', wCells: 2, hCells: Math.ceil(N / 2) }, // tall and wide
            ]

            for(let shape of shapes) {
                // compute the bbox for the shape
                let lngSpan = shape.wCells * lonStepDeg
                let latSpan = shape.hCells * latStepDeg
                let bbox = {
                    minLat: uniqueLat - latSpan / 2,
                    maxLat: uniqueLat + latSpan / 2,
                    minLng: uniqueLng - lngSpan / 2,
                    maxLng: uniqueLng + lngSpan / 2
                }

                // skip if the bbox is out of bounds
                if(bbox.minLat < -90 || bbox.maxLat > 90 || bbox.minLng < -180 || bbox.maxLng > 180) {
                    // console.log(`Precision ${precision}, ${shape.tag}, N=${N}: bbox is out of bounds: minLat=${bbox.minLat}, maxLat=${bbox.maxLat}, minLng=${bbox.minLng}, maxLng=${bbox.maxLng}`)
                    continue;
                }

                // compute the effective number of cells in the bbox
                let lngSpan_effective = bbox.maxLng - bbox.minLng
                let latSpan_effective = bbox.maxLat - bbox.minLat
                let wCells_effective = Math.ceil(lngSpan_effective / lonStepDeg)
                let hCells_effective = Math.ceil(latSpan_effective / latStepDeg)
                let estimatedCells = wCells_effective * hCells_effective

                let successExpected = estimatedCells <= MAX_PINGAREA_GEOHASHES; // true: 200, false: 413
                let res = http.get(`${BASE_URL}/pingArea?precision=${precision}&minLat=${bbox.minLat}&maxLat=${bbox.maxLat}&minLng=${bbox.minLng}&maxLng=${bbox.maxLng}`)
                check(res, { [`Precision ${precision}, ${shape.tag}, N=${N}: status is ${successExpected ? 200 : 413}`]: () => res.status === (successExpected ? 200 : 413) })
            }
        }
    }

}

export function handleSummary(data) {
    return {
        'stdout': JSON.stringify(data.metrics, null, 2),
        'outputs/explosion_summary.json': JSON.stringify(data, null, 2),
    }
}

function geohashCellDimsDegrees(precision) {
    // returns the width and height of a geohash cell in degrees at a given precision
    // each geohash character is base32 => 5 bits. bits alternate lon/lat starting with lon
    const bits = precision * 5;
    const lonBits = Math.floor((bits + 1) / 2); // starts at lon, so lon gets the extra bit
    const latBits = Math.floor(bits / 2);

    const lonDeg = 360.0 / Math.pow(2, lonBits); // 360º / 2^(lonBits) -> degrees per longitude cell
    const latDeg = 180.0 / Math.pow(2, latBits); // 180º / 2^(latBits) -> degrees per latitude cell
    return [lonDeg, latDeg];
}