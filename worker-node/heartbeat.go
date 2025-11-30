package main

import (
	"context"
	pb "hello_go/proto"
	"log"
	"os"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func new_grpc_client(gatewayAddress string) (*grpc.ClientConn, pb.GatewayClient) {
	conn, err := grpc.NewClient(gatewayAddress, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("failed to dial: %v", err)
	}
	return conn, pb.NewGatewayClient(conn)
}

func send_heartbeat(client pb.GatewayClient) {
	workerId := uuid.New().String()
	hostname, _ := os.Hostname() // hostname used as address with docker compose
	port := os.Getenv("HEARTBEAT_PORT")
	if port == "" {
		port = "50051"
	}
	fullAddress := hostname + ":" + port

	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for ; ; <-ticker.C {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err := client.Heartbeat(ctx, &pb.HeartbeatRequest{WorkerId: workerId, Address: fullAddress})
		if err != nil {
			log.Printf("failed to send heartbeat: %v", err)
		}
		log.Printf("heartbeat sent")

		cancel()
	}
}
