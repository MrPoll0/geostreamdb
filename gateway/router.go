package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	pb "geostreamdb/proto"
)

type gpsPing struct {
	Latitude  float64 `json:"lat"`
	Longitude float64 `json:"lng"`
}

var MAX_GH_PRECISION = 8
var MAX_PINGAREA_GEOHASHES = int64(5000)
var SHARDING_PRECISION = 6

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func setup_router() *chi.Mux {
	router := chi.NewRouter()
	router.Use(corsMiddleware)
	if os.Getenv("DEBUG") == "true" {
		router.Use(middleware.Logger)
	}

	router.Get("/ping", getPing)
	router.Post("/ping", postPing)

	router.Get("/pingArea", getPingArea)

	return router
}

func postPing(w http.ResponseWriter, r *http.Request) {
	var newGpsPing gpsPing

	if err := json.NewDecoder(r.Body).Decode(&newGpsPing); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Invalid request body"))
		return
	}

	gh := geohashEncodeWithPrecision(newGpsPing.Latitude, newGpsPing.Longitude, MAX_GH_PRECISION)
	truncatedGh := gh[:6] // truncate to precision 6 for sharding

	// get the address of the worker node responsible for this geohash
	targetAddr := state.GetNodeAddress(truncatedGh)
	if targetAddr == "" {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("No workers available"))
		return
	}

	// get a connection to the worker node (pool of connections, do not close)
	conn, err := state.GetConn(targetAddr)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Failed to connect to worker"))
		return
	}

	client := pb.NewWorkerClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, err = client.SendPing(ctx, &pb.PingRequest{Geohash: gh})
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Failed to contact worker"))
		return
	}

	w.WriteHeader(http.StatusCreated)
	w.Write([]byte("Ping sent, geohash: " + gh))
}

// temporary: to get count of specific coord (max geohash precision)
func getPing(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	latQ := query.Get("lat")
	lngQ := query.Get("lng")

	if latQ == "" || lngQ == "" {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Missing query parameters"))
		return
	}

	// parse latitude and longitude
	lat, err := strconv.ParseFloat(latQ, 64)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Invalid latitude"))
		return
	}
	lng, err := strconv.ParseFloat(lngQ, 64)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Invalid longitude"))
		return
	}

	gh := geohashEncodeWithPrecision(lat, lng, MAX_GH_PRECISION)
	truncatedGh := gh[:6] // truncate to precision 6 for sharding

	// get the address of the worker node responsible for this geohash
	targetAddr := state.GetNodeAddress(truncatedGh)
	if targetAddr == "" {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("No workers available"))
		return
	}

	// get a connection to the worker node (pool of connections, do not close)
	conn, err := state.GetConn(targetAddr)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Failed to connect to worker"))
		return
	}

	client := pb.NewWorkerClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	v, err := client.GetPings(ctx, &pb.GetPingsRequest{Geohash: gh})
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Failed to get pings from worker"))
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]int64{"count": v.Count, "timestamp": v.Timestamp})
}

func getPingArea(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	minLatQ := query.Get("minLat")
	maxLatQ := query.Get("maxLat")
	minLngQ := query.Get("minLng")
	maxLngQ := query.Get("maxLng")
	precisionQ := query.Get("precision")

	if minLatQ == "" || maxLatQ == "" || minLngQ == "" || maxLngQ == "" || precisionQ == "" {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Missing query parameters"))
		return
	}

	// parse query parameters
	minLat, err := strconv.ParseFloat(minLatQ, 64)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Invalid minimum latitude"))
		return
	}
	maxLat, err := strconv.ParseFloat(maxLatQ, 64)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Invalid maximum latitude"))
		return
	}
	minLng, err := strconv.ParseFloat(minLngQ, 64)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Invalid minimum longitude"))
		return
	}
	maxLng, err := strconv.ParseFloat(maxLngQ, 64)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Invalid maximum longitude"))
		return
	}
	precision, err := strconv.Atoi(precisionQ)
	if err != nil || precision < 1 || precision > MAX_GH_PRECISION {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Invalid precision"))
		return
	}

	if minLat < -90 || maxLat > 90 || minLat > maxLat || minLng < -180 || maxLng > 180 || minLng > maxLng {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Invalid bounding box"))
		return
	}

	// safety check: bound how many cells the query precision would create for this bbox
	estimated, _, _ := estimateGeohashCoverCount(minLat, maxLat, minLng, maxLng, precision)
	if estimated > MAX_PINGAREA_GEOHASHES {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		w.Write([]byte("Requested area too large for precision"))
		return
	}

	bboxW, bboxH := bboxDimsMeters(minLat, maxLat, minLng, maxLng)
	precUsed, cellW, cellH, ok := chooseAggregatedPrecision(precision, minLat, maxLat, minLng, maxLng)
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Bounding box too small for available precisions"))
		return
	}

	cover := geohashCoverSet(minLat, maxLat, minLng, maxLng, precUsed)

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]any{
		"requestedPrecision": precision,
		"precisionUsed":      precUsed,
		"geohashes":          cover,
		"bboxMeters": map[string]float64{
			"width":  bboxW,
			"height": bboxH,
			"area":   bboxW * bboxH,
		},
		"cellMeters": map[string]float64{
			"width":  cellW,
			"height": cellH,
			"area":   cellW * cellH,
		},
	})
}
