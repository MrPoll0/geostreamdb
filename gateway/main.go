package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/mmcloughlin/geohash"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pb "hello_go/proto"
)

type gpsPing struct {
	Latitude  float64 `json:"lat"`
	Longitude float64 `json:"lng"`
}

func new_grpc_client() (*grpc.ClientConn, pb.WorkerClient) {
	conn, err := grpc.NewClient("worker-node:50051", grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("failed to dial: %v", err)
	}
	return conn, pb.NewWorkerClient(conn)
}

func setup_router(client pb.WorkerClient) *chi.Mux {
	router := chi.NewRouter()
	router.Use(middleware.Logger)

	router.Get("/ping", func(w http.ResponseWriter, r *http.Request) { getPings(w, r, client) })
	router.Post("/ping", func(w http.ResponseWriter, r *http.Request) { postPing(w, r, client) })

	return router
}

func main() {
	conn, client := new_grpc_client()
	defer conn.Close()

	router := setup_router(client)

	httpPort := os.Getenv("PORT")
	if httpPort == "" {
		httpPort = "8080"
	}
	log.Printf("Server listening on port %s", httpPort)
	if err := http.ListenAndServe(":"+httpPort, router); err != nil {
		log.Fatalf("failed to serve: %v", err)
	}
}

func postPing(w http.ResponseWriter, r *http.Request, client pb.WorkerClient) {
	var newGpsPing gpsPing

	if err := json.NewDecoder(r.Body).Decode(&newGpsPing); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Invalid request body"))
		return
	}

	gh := geohash.Encode(newGpsPing.Latitude, newGpsPing.Longitude)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := client.SendPing(ctx, &pb.PingRequest{Geohash: gh})
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Failed to contact worker"))
		return
	}

	w.WriteHeader(http.StatusCreated)
	w.Write([]byte("Ping sent, geohash: " + gh))
}

func getPings(w http.ResponseWriter, _ *http.Request, client pb.WorkerClient) {
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
}
