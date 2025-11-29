package main

import (
	"context"
	"log"
	"net"
	"os"
	"time"

	pb "hello_go/proto"

	"google.golang.org/grpc"
)

var pings = []*pb.Ping{}

type server struct {
	pb.UnimplementedWorkerServer
}

func (s *server) SendPing(ctx context.Context, req *pb.PingRequest) (*pb.PingResponse, error) {
	log.Printf("Received ping request for geohash: %s", req.Geohash)
	pings = append(pings, &pb.Ping{Geohash: req.Geohash, Timestamp: time.Now().Unix()})
	log.Printf("Added ping to list: %v", pings)
	return &pb.PingResponse{Success: true}, nil
}

func (s *server) GetPings(ctx context.Context, req *pb.GetPingsRequest) (*pb.GetPingsResponse, error) {
	return &pb.GetPingsResponse{Pings: pings}, nil
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "50051"
	}
	lis, err := net.Listen("tcp", ":"+port)
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}

	s := grpc.NewServer()
	pb.RegisterWorkerServer(s, &server{})
	log.Printf("server listening at %v", lis.Addr())
	if err := s.Serve(lis); err != nil {
		log.Fatalf("failed to serve: %v", err)
	}
}
