package main

import (
	"log"
	"net/http"
	"os"
	"time"
)

func main() {
	// (grpc client) heartbeats to registry for service discovery
	registryAddress := os.Getenv("REGISTRY_ADDRESS")
	if registryAddress == "" {
		registryAddress = "registry:50051"
	}
	conn, client := new_grpc_client(registryAddress)
	defer conn.Close()
	go send_heartbeat(client, registryAddress)

	// (grpc server) heartbeat communication
	go setup_heartbeat_listener()
	// cleanup dead nodes loop
	cleanup_ttl := 10 * time.Second
	go state.cleanupDeadNodes(cleanup_ttl, cleanup_ttl/2)

	// (http server) ping reception -> (grpc client) forwarding to worker nodes
	router := setup_router()

	httpPort := os.Getenv("PORT")
	if httpPort == "" {
		httpPort = "8080"
	}
	log.Printf("HTTP server listening on port %s", httpPort)
	if err := http.ListenAndServe(":"+httpPort, router); err != nil {
		log.Fatalf("failed to serve: %v", err)
	}
}
