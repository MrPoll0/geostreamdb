package main

import (
	"context"
	pb "geostreamdb/proto"
	"log"
	"time"
)

func observeGRPC(method string, err error, start time.Time) {
	result := "success"
	if err != nil {
		result = "failure"
	}
	Metrics.gRPCRequestsTotal.WithLabelValues(method, result).Inc()
	Metrics.gRPCLatency.WithLabelValues(method).Observe(time.Since(start).Seconds())
}

type gatewayHeartbeatServer struct {
	pb.UnimplementedGatewayServer
}

func (s *gatewayHeartbeatServer) Heartbeat(ctx context.Context, req *pb.HeartbeatRequest) (*pb.HeartbeatResponse, error) {
	// forward worker heartbeat to all gateways (to maintain same ring state)

	// log.Printf("received worker heartbeat from: %s (worker id: %s)", req.Address, req.WorkerId)

	connections := registryState.getAllConnections()
	for _, conn := range connections {
		client := pb.NewGatewayClient(conn)
		timeoutCtx, cancel := context.WithTimeout(context.Background(), time.Second)

		start := time.Now()
		_, err := client.Heartbeat(timeoutCtx, req)
		cancel()
		observeGRPC("Gateway.Heartbeat", err, start)
		if err != nil {
			log.Printf("failed to forward heartbeat to gateway: %v", err)
		}
		// log.Printf("heartbeat forwarded to gateway: %s (worker id: %s)", conn.Target(), req.WorkerId)
	}

	return &pb.HeartbeatResponse{Acknowledged: true}, nil
}
