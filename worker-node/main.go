package main

import (
	"log"
	"net"
	"os"

	pb "geostreamdb/proto"

	"google.golang.org/grpc"
)

func main() {
	// (grpc client) heartbeats to gateway (registry -> gateway) for service discovery
	registryAddress := os.Getenv("REGISTRY_ADDRESS")
	if registryAddress == "" {
		registryAddress = "registry:50051"
	}
	conn, client := new_grpc_client(registryAddress)
	defer conn.Close()
	go send_heartbeat(client)

	// (grpc server) ping communication
	go cleanupTimeBuffer()

	port := os.Getenv("PORT")
	if port == "" {
		port = "50051"
	}
	lis, err := net.Listen("tcp", ":"+port)
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}

	s := grpc.NewServer()
	pb.RegisterWorkerServer(s, &grpcServer{})
	log.Printf("grpc server listening at %v", lis.Addr())
	if err := s.Serve(lis); err != nil {
		log.Fatalf("failed to serve: %v", err)
	}
}
