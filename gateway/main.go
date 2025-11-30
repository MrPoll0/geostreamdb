package main

import (
	"log"
	"net/http"
	"os"
	"time"
)

func main() {
	// (grpc server) heartbeat communication
	go setup_heartbeat_listener()
	// cleanup dead nodes loop
	go state.cleanupDeadNodes(10 * time.Second)

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
