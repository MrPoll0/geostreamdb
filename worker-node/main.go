package main

import (
	"context"
	"log"
	"net"
	"os"
	"sync"
	"time"

	pb "hello_go/proto"

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

var (
	pings     = make(map[string]int64) // geohash -> count
	pingMutex sync.RWMutex
)

type grpcServer struct {
	pb.UnimplementedWorkerServer
}

func (s *grpcServer) SendPing(ctx context.Context, req *pb.PingRequest) (*pb.PingResponse, error) {
	log.Printf("Received ping request for geohash: %s", req.Geohash)

	pingMutex.Lock()
	pings[req.Geohash]++
	log.Printf("Added ping count: %v", pings)
	pingMutex.Unlock()

	return &pb.PingResponse{Success: true}, nil
}

/*func (s *grpcServer) GetPings(ctx context.Context, req *pb.GetPingsRequest) (*pb.GetPingsResponse, error) {
	log.Printf("Received get pings request")

	pingMutex.RLock()
	res := make([]*pb.Ping, len(pings))
	copy(res, pings)
	pingMutex.RUnlock()

	return &pb.GetPingsResponse{Pings: res}, nil
}*/

func main() {
	// heartbeats to gateway for service discovery
	gatewayAddress := os.Getenv("GATEWAY_ADDRESS")
	if gatewayAddress == "" {
		gatewayAddress = "gateway:50051"
	}
	conn, client := new_grpc_client(gatewayAddress)
	defer conn.Close()
	go send_heartbeat(client)

	// grpc server for ping communication
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
