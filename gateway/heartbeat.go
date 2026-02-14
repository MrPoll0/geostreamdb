package main

import (
	"context"
	pb "geostreamdb/proto"
	"log"
	"os"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func new_grpc_client(registryAddress string) (*grpc.ClientConn, pb.RegistryClient) {
	conn, err := grpc.NewClient(registryAddress, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("failed to dial: %v", err)
	}
	return conn, pb.NewRegistryClient(conn)
}

func send_heartbeat(client pb.RegistryClient, registryAddress string) {
	gatewayId := uuid.New().String()
	// use pod IP if available (Kubernetes), otherwise use hostname (Docker Compose)
	address := os.Getenv("GATEWAY_ADDRESS")
	if address == "" {
		hostname, _ := os.Hostname()
		address = hostname
	}
	port := os.Getenv("GATEWAY_PORT")
	if port == "" {
		port = "50051"
	}
	fullAddress := address + ":" + port

	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for ; ; <-ticker.C {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		start := time.Now()
		_, err := client.Heartbeat(ctx, &pb.RegistryHeartbeatRequest{GatewayId: gatewayId, Address: fullAddress})
		cancel()
		observeGRPC("Registry.Heartbeat", registryAddress, err, start)

		if err != nil {
			log.Printf("failed to send heartbeat to registry: %v", err)
		}
		// log.Printf("heartbeat sent to registry: %s (gateway id: %s)", fullAddress, gatewayId)
	}
}
