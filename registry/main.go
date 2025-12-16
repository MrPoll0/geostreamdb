package main

import (
	"log"
	"net"
	"os"
	"time"

	pb "geostreamdb/proto"

	"google.golang.org/grpc"
)

var GATEWAY_CLEANUP_TTL = 10 * time.Second
var GATEWAY_CLEANUP_TICK_TIME = 5 * time.Second

func main() {
	// (grpc server) worker heartbeat and gateway registration receiver
	go registryState.cleanupDeadGateways(GATEWAY_CLEANUP_TTL, GATEWAY_CLEANUP_TICK_TIME)
	port := os.Getenv("PORT")
	if port == "" {
		port = "50051"
	}
	lis, err := net.Listen("tcp", ":"+port)
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}

	s := grpc.NewServer()
	pb.RegisterGatewayServer(s, &gatewayHeartbeatServer{}) // worker heartbeat receiver
	pb.RegisterRegistryServer(s, &registryServer{})        // gateway registration receiver
	log.Printf("grpc server listening at %v", lis.Addr())
	if err := s.Serve(lis); err != nil {
		log.Fatalf("failed to serve: %v", err)
	}
}
