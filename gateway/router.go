package main

import (
	"context"
	"encoding/json"
	"net/http"
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

func setup_router() *chi.Mux {
	router := chi.NewRouter()
	router.Use(middleware.Logger)

	// router.Get("/ping", getPings)
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

	gh := geohash.Encode(newGpsPing.Latitude, newGpsPing.Longitude)
	// truncate geohash for better locality
	if len(gh) > 7 {
		gh = gh[:7]
	}

	// get the address of the worker node responsible for this geohash
	targetAddr := state.GetNodeAddress(gh)
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

/*func getPings(w http.ResponseWriter, _ *http.Request) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	v, err := client.GetPings(ctx, &pb.GetPingsRequest{})
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Failed to get pings"))
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(v.Pings)
}*/
