package main

import (
	"log"
	"net"
	"os"

	pb "geostreamdb/proto"

	"google.golang.org/grpc"
)

func main() {
	// (grpc client) heartbeats to gateway for service discovery
	gatewayAddress := os.Getenv("GATEWAY_ADDRESS")
	if gatewayAddress == "" {
		gatewayAddress = "gateway:50051"
	}
	conn, client := new_grpc_client(gatewayAddress)
	defer conn.Close()
	go send_heartbeat(client)

	// (grpc server) ping communication
	go cleanupTimePingBucket()

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
