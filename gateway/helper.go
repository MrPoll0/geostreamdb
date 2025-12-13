package main

import (
	"math"
	"sort"

	"github.com/mmcloughlin/geohash"
)

const EARTH_RADIUS_METERS = 6371008.8

type ghBbox struct {
	minLat float64
	maxLat float64
	minLng float64
	maxLng float64
}

func (a ghBbox) intersects(b ghBbox) bool {
	return !(a.maxLat < b.minLat || a.minLat > b.maxLat || a.maxLng < b.minLng || a.minLng > b.maxLng)
}

var geohashBase32 = "0123456789bcdefghjkmnpqrstuvwxyz"

func geohashDecodeBbox(gh string) (ghBbox, bool) {
	if gh == "" {
		return ghBbox{}, false
	}

	var charmap [256]byte
	for i := range charmap {
		charmap[i] = 0xFF
	}
	for i := 0; i < len(geohashBase32); i++ {
		charmap[geohashBase32[i]] = byte(i)
	}

	minLat, maxLat := -90.0, 90.0
	minLng, maxLng := -180.0, 180.0
	isLng := true // geohash bits start with longitude

	for i := 0; i < len(gh); i++ {
		c := gh[i]
		// normalize ASCII uppercase -> lowercase (geohash alphabet is lowercase)
		if c >= 'A' && c <= 'Z' {
			c = c + ('a' - 'A')
		}
		v := charmap[c] // base32 char -> [0, 31]
		if v == 0xFF {
			return ghBbox{}, false
		}
		for bit := 4; bit >= 0; bit-- { // a geohash character is 5 bits (base 32). check high -> low and halve the range interleaving lon/lat
			mask := byte(1 << uint(bit))
			if isLng {
				// halve lng range
				mid := (minLng + maxLng) / 2
				if v&mask != 0 {
					minLng = mid // keep upper half
				} else {
					maxLng = mid // keep lower half
				}
			} else {
				// halve lat range
				mid := (minLat + maxLat) / 2
				if v&mask != 0 {
					minLat = mid // keep upper half
				} else {
					maxLat = mid // keep lower half
				}
			}
			isLng = !isLng // alternate lon/lat
		}
	}

	// the remaining values are the geohash cell box
	return ghBbox{minLat: minLat, maxLat: maxLat, minLng: minLng, maxLng: maxLng}, true
}

func geohashEncodeWithPrecision(lat, lng float64, precision int) string {
	gh := geohash.Encode(lat, lng)
	if precision <= 0 {
		return ""
	}
	if len(gh) >= precision {
		return gh[:precision]
	}
	return gh
}

func deg2rad(deg float64) float64 { return deg * math.Pi / 180 }

func haversineMeters(lat1, lng1, lat2, lng2 float64) float64 {
	// returns the distance in meters between two points on the Earth's surface by using the haversine formula
	lat1r := deg2rad(lat1)
	lat2r := deg2rad(lat2)
	dlat := deg2rad(lat2 - lat1)
	dlng := deg2rad(lng2 - lng1)

	sinDLat := math.Sin(dlat / 2)
	sinDLng := math.Sin(dlng / 2)
	a := sinDLat*sinDLat + math.Cos(lat1r)*math.Cos(lat2r)*sinDLng*sinDLng
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
	return EARTH_RADIUS_METERS * c
}

func latForMaxWidthMeters(minLat, maxLat float64) float64 {
	// meters per degree longitude is maximized closest to the equator (cos(lat))
	if (minLat <= 0 && maxLat >= 0) || (maxLat <= 0 && minLat >= 0) {
		return 0
	}
	if math.Abs(minLat) < math.Abs(maxLat) {
		return minLat
	}
	return maxLat
}

func latForMinWidthMeters(minLat, maxLat float64) float64 {
	// meters per degree longitude is minimized farthest from the equator (cos(lat)),
	// which maximizes the number of longitudinal cells needed to cover a bbox
	if math.Abs(minLat) > math.Abs(maxLat) {
		return minLat
	}
	return maxLat
}

func bboxDimsMeters(minLat, maxLat, minLng, maxLng float64) (widthMeters, heightMeters float64) {
	// returns the width and height of a bounding box in meters
	latForWidth := latForMaxWidthMeters(minLat, maxLat)
	widthMeters = haversineMeters(latForWidth, minLng, latForWidth, maxLng) // choose latitude closest to the equator to maximize width
	midLng := (minLng + maxLng) / 2                                         // distance north/south doesn't depend on longitude, so midpoint is stable and simple
	heightMeters = haversineMeters(minLat, midLng, maxLat, midLng)
	return widthMeters, heightMeters
}

func estimateGeohashCoverCount(minLat, maxLat, minLng, maxLng float64, precision int) (count int64, cellsWide int64, cellsHigh int64) {
	// returns an estimation of the number of geohashes that would cover a bounding box at a given precision
	if precision <= 0 {
		return 0, 0, 0
	}
	bboxW, bboxH := bboxDimsMeters(minLat, maxLat, minLng, maxLng)
	// for a conservative (high) count estimate, use the smallest cell width within the bbox
	latWidth := latForMinWidthMeters(minLat, maxLat) // furthest from the equator -> smallest cell width
	cellW, cellH := geohashCellDimsMeters(precision, latWidth)
	if cellW <= 0 || cellH <= 0 {
		return 0, 0, 0
	}

	w := int64(math.Ceil(bboxW / cellW))
	h := int64(math.Ceil(bboxH / cellH))
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	return w * h, w, h
}

func geohashCellDimsDegrees(precision int) (lonDeg, latDeg float64) {
	// returns the width and height of a geohash cell in degrees at a given precision
	// each geohash character is base32 => 5 bits. bits alternate lon/lat starting with lon
	bits := precision * 5
	lonBits := (bits + 1) / 2 // starts at lon, so lon gets the extra bit
	latBits := bits / 2

	lonDeg = 360.0 / float64(uint64(1)<<uint(lonBits)) // 360º / 2^(lonBits) -> degrees per longitude cell
	latDeg = 180.0 / float64(uint64(1)<<uint(latBits)) // 180º / 2^(latBits) -> degrees per latitude cell
	return lonDeg, latDeg
}

func geohashCellDimsMeters(precision int, latForWidth float64) (widthMeters, heightMeters float64) {
	// returns the width and height of a geohash cell in meters at a given precision
	lonDeg, latDeg := geohashCellDimsDegrees(precision)
	heightMeters = deg2rad(latDeg) * EARTH_RADIUS_METERS
	widthMeters = deg2rad(lonDeg) * EARTH_RADIUS_METERS * math.Cos(deg2rad(latForWidth))
	return widthMeters, heightMeters
}

func chooseAggregatedPrecision(requested int, minLat, maxLat, minLng, maxLng float64) (precisionUsed int, cellWidthMeters, cellHeightMeters float64, ok bool) {
	// returns the aggregated precision (and cell size) for a bounding box given a requested precision so that cell size is as large as possible without exceeding the bbox
	bboxWidth, bboxHeight := bboxDimsMeters(minLat, maxLat, minLng, maxLng)
	latWidth := latForMaxWidthMeters(minLat, maxLat)

	// prefer coarser precision (bigger cells) but only within requested-2..requested, if possible
	start := requested - 2 // TODO: unbounded or keep a hardcoded bound? need to review amount of leaf nodes searched at worker node if unbounded
	if start < 1 {
		start = 1
	}
	for p := start; p <= requested; p++ {
		wm, hm := geohashCellDimsMeters(p, latWidth)
		if wm <= bboxWidth && hm <= bboxHeight {
			return p, wm, hm, true
		}
	}

	// if bbox is smaller than requested cell, fall back to finer precisions until it fits
	for p := requested + 1; p <= MAX_GH_PRECISION; p++ {
		wm, hm := geohashCellDimsMeters(p, latWidth)
		if wm <= bboxWidth && hm <= bboxHeight {
			return p, wm, hm, true
		}
	}

	return 0, 0, 0, false
}

func geohashCoverSet(minLat, maxLat, minLng, maxLng float64, precision int) []string {
	query := ghBbox{minLat: minLat, maxLat: maxLat, minLng: minLng, maxLng: maxLng}

	// seed from the bbox center, then flood-fill neighbors whose cell bbox intersects query
	seedLat := (minLat + maxLat) / 2
	seedLng := (minLng + maxLng) / 2
	seed := geohashEncodeWithPrecision(seedLat, seedLng, precision)
	if seed == "" {
		return nil
	}

	lonStepDeg, latStepDeg := geohashCellDimsDegrees(precision)
	if lonStepDeg <= 0 || latStepDeg <= 0 {
		return nil
	}

	// BFS to find all geohashes that intersect with the query bbox
	visited := make(map[string]struct{})
	inSet := make(map[string]struct{})
	queue := []string{seed}

	for len(queue) > 0 {
		gh := queue[0]
		queue = queue[1:]

		if _, ok := visited[gh]; ok {
			continue
		}
		visited[gh] = struct{}{}

		cell, ok := geohashDecodeBbox(gh)
		if !ok || !cell.intersects(query) {
			continue
		}

		inSet[gh] = struct{}{}

		// enqueue 8 neighbors by shifting the cell center by 1 cell in each direction
		cLat := (cell.minLat + cell.maxLat) / 2
		cLng := (cell.minLng + cell.maxLng) / 2

		for _, dLat := range []float64{-1, 0, 1} {
			for _, dLng := range []float64{-1, 0, 1} {
				if dLat == 0 && dLng == 0 {
					continue
				}
				nLat := cLat + dLat*latStepDeg
				nLng := cLng + dLng*lonStepDeg
				if nLat < -90 || nLat > 90 || nLng < -180 || nLng > 180 {
					continue
				}
				ngh := geohashEncodeWithPrecision(nLat, nLng, precision)
				if ngh == "" {
					continue
				}
				if _, ok := visited[ngh]; ok {
					continue
				}
				queue = append(queue, ngh)
			}
		}
	}

	out := make([]string, 0, len(inSet))
	for gh := range inSet {
		out = append(out, gh)
	}
	sort.Strings(out)
	return out
}
