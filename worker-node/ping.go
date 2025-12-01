package main

import (
	"context"
	pb "geostreamdb/proto"
	"log"
	"sync"
)

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
