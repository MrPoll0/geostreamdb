package main

import (
	"log"
	"net/http"
	"os"
)

func main() {
	go setup_heartbeat_listener()

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
