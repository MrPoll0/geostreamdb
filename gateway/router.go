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
	truncatedGh := gh[:SHARDING_PRECISION] // truncate to sharding precision

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
	truncatedGh := gh[:SHARDING_PRECISION] // truncate to sharding precision

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

	precUsed, _, _, ok := chooseAggregatedPrecision(precision, minLat, maxLat, minLng, maxLng)
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Bounding box too small for available precisions"))
		return
	}

	cover := geohashCoverSet(minLat, maxLat, minLng, maxLng, precUsed)

	// TEST: to color geohash by server
	type ExtendedGetPingAreaResponse struct {
		*pb.GetPingAreaResponse
		Server string
	}

	results := make([]*ExtendedGetPingAreaResponse, 0)
	if precUsed >= SHARDING_PRECISION {
		// we can find shards responsible for these geohashes. find and group them

		// group geohashes by shard
		grouped := make(map[string][]string)
		for _, geohash := range cover {
			tarGh := geohash[:SHARDING_PRECISION]
			targetAddr := state.GetNodeAddress(tarGh)
			if targetAddr == "" {
				continue
			}
			grouped[targetAddr] = append(grouped[targetAddr], geohash)
		}

		// for every key (node address), get the ping area for its assigned geohashes
		for targetAddr, geohashes := range grouped {
			conn, err := state.GetConn(targetAddr)
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte("Failed to connect to worker"))
				return
			}

			client := pb.NewWorkerClient(conn)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()

			v, err := client.GetPingArea(ctx, &pb.GetPingAreaRequest{Precision: int32(precision), AggPrecision: int32(precUsed), MinLat: minLat, MaxLat: maxLat, MinLng: minLng, MaxLng: maxLng, Geohashes: geohashes})
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte("Failed to get ping area from worker"))
				return
			}
			results = append(results, &ExtendedGetPingAreaResponse{GetPingAreaResponse: v, Server: targetAddr})
		}
	} else {
		// geohashes will be spread across multiple shards. broadcast query to all nodes

		seenServers := make(map[string]struct{})
		for _, node := range state.ring {
			if _, seen := seenServers[node.Server]; seen { // avoid repetition because of virtual nodes
				continue
			}
			seenServers[node.Server] = struct{}{}

			conn, err := state.GetConn(node.Server)
			if err != nil {
				continue
			}
			client := pb.NewWorkerClient(conn)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()

			v, err := client.GetPingArea(ctx, &pb.GetPingAreaRequest{Precision: int32(precision), AggPrecision: int32(precUsed), MinLat: minLat, MaxLat: maxLat, MinLng: minLng, MaxLng: maxLng, Geohashes: cover})
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte("Failed to get ping area from worker"))
				return
			}
			results = append(results, &ExtendedGetPingAreaResponse{GetPingAreaResponse: v, Server: node.Server})
		}
	}

	type ExtendedPingAreaCount struct {
		Count  int64
		Server string
	}

	// combine all results into a single map of geohash -> count
	combined := make(map[string]*ExtendedPingAreaCount)
	for _, result := range results {
		for _, count := range result.Counts {
			if _, exists := combined[count.Geohash]; !exists {
				combined[count.Geohash] = &ExtendedPingAreaCount{Count: 0, Server: result.Server}
			}
			combined[count.Geohash].Count += count.Count
		}
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(combined)
}
