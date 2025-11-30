package main

import (
	"context"
	"log"
	"net"
	"os"

	"google.golang.org/grpc"

	pb "hello_go/proto"
)

type grpcServer struct {
	pb.UnimplementedGatewayServer
}

func (s *grpcServer) Heartbeat(ctx context.Context, req *pb.HeartbeatRequest) (*pb.HeartbeatResponse, error) {
	state.addNode(req.WorkerId, req.Address)
	return &pb.HeartbeatResponse{Acknowledged: true}, nil
}

func setup_heartbeat_listener() {
	port := os.Getenv("HEARTBEAT_PORT")
	if port == "" {
		port = "50051"
	}
	lis, err := net.Listen("tcp", ":"+port)
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}

	s := grpc.NewServer()
	pb.RegisterGatewayServer(s, &grpcServer{})
	log.Printf("grpc server listening at %v", lis.Addr())
	if err := s.Serve(lis); err != nil {
		log.Fatalf("failed to serve: %v", err)
	}
}
