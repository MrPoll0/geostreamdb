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
	"github.com/mmcloughlin/geohash"

	pb "geostreamdb/proto"
)

type gpsPing struct {
	Latitude  float64 `json:"lat"`
	Longitude float64 `json:"lng"`
}

func getGeohash(lat float64, lng float64) string {
	// returns a geohash of precision 8

	gh := geohash.Encode(lat, lng)
	// truncate geohash for better locality
	if len(gh) >= 8 {
		gh = gh[:8]
	}
	return gh
}

func setup_router() *chi.Mux {
	router := chi.NewRouter()
	if os.Getenv("DEBUG") == "true" {
		router.Use(middleware.Logger)
	}

	router.Get("/ping/{lat}/{lng}", getPings)
	router.Post("/ping", postPing)

	return router
}

func postPing(w http.ResponseWriter, r *http.Request) {
	var newGpsPing gpsPing

	if err := json.NewDecoder(r.Body).Decode(&newGpsPing); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Invalid request body"))
		return
	}

	gh := getGeohash(newGpsPing.Latitude, newGpsPing.Longitude) // precision 8
	truncatedGh := gh[:6]                                       // truncate to precision 6

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

func getPings(w http.ResponseWriter, r *http.Request) {
	// parse latitude and longitude
	lat, err := strconv.ParseFloat(chi.URLParam(r, "lat"), 64)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Invalid latitude"))
		return
	}
	lng, err := strconv.ParseFloat(chi.URLParam(r, "lng"), 64)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Invalid longitude"))
		return
	}

	gh := getGeohash(lat, lng) // precision 8
	truncatedGh := gh[:6]      // truncate to precision 6

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
