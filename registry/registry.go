package main

import (
	"context"
	pb "geostreamdb/proto"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type registryServer struct {
	pb.UnimplementedRegistryServer
}

type RegistryState struct {
	Gateways    map[string]string
	Mutex       sync.RWMutex
	Clients     map[string]*grpc.ClientConn
	ClientMutex sync.RWMutex
	lastSeen    map[string]int64
}

var registryState = &RegistryState{Gateways: make(map[string]string), Clients: make(map[string]*grpc.ClientConn), lastSeen: make(map[string]int64)}

func (s *registryServer) Heartbeat(ctx context.Context, req *pb.RegistryHeartbeatRequest) (*pb.RegistryHeartbeatResponse, error) {
	// gateway heartbeats

	start := time.Now()
	var err error // for error handling, not implemented yet
	defer func() {
		observeGRPC("Registry.Heartbeat", err, start)
	}()

	// log.Printf("received gateway heartbeat from: %s (gateway id: %s)", req.Address, req.GatewayId)

	registryState.Mutex.RLock()
	v, gExists := registryState.Gateways[req.GatewayId]
	registryState.Mutex.RUnlock()

	if !gExists || v != req.Address { // new gateway or different address
		// close and delete old client connection if it exists
		registryState.ClientMutex.Lock()
		conn, cExists := registryState.Clients[v]
		if (cExists && conn != nil) && (gExists && v != "" && v != req.Address) {
			conn.Close()
			delete(registryState.Clients, v)
		}
		registryState.ClientMutex.Unlock()

		// gateway registration or update -> setup new client connection for that address
		registryState.ClientMutex.RLock()
		_, ngExists := registryState.Clients[req.Address]
		registryState.ClientMutex.RUnlock()
		if !ngExists {
			conn, err := grpc.NewClient(req.Address, grpc.WithTransportCredentials(insecure.NewCredentials()))
			if err != nil {
				return nil, err
			}

			registryState.ClientMutex.Lock()
			if _, exists := registryState.Clients[req.Address]; !exists { // double check to avoid race condition
				registryState.Clients[req.Address] = conn
			} else {
				conn.Close()
			}
			registryState.ClientMutex.Unlock()
		}
	}

	registryState.Mutex.Lock()
	registryState.Gateways[req.GatewayId] = req.Address
	registryState.lastSeen[req.GatewayId] = time.Now().Unix()
	registryState.Mutex.Unlock()

	// track registered gateways (only additions, not updates)
	if !gExists {
		Metrics.registeredGatewaysTotal.Inc()
	}

	return &pb.RegistryHeartbeatResponse{Acknowledged: true}, err
}

func (g *RegistryState) cleanupDeadGateways(ttl time.Duration, tick_time time.Duration) {
	ticker := time.NewTicker(tick_time)
	defer ticker.Stop()

	for range ticker.C {
		g.Mutex.Lock()

		now := time.Now().Unix()
		for gatewayId, lastSeen := range g.lastSeen {
			if now-lastSeen > int64(ttl.Seconds()) {
				server := g.Gateways[gatewayId]
				delete(g.Gateways, gatewayId)
				delete(g.lastSeen, gatewayId)

				// TODO (here and in gateway ring): separate id and connection cleanup to avoid blocking Mutex lock while waiting for ClientMutex
				// close and delete connection to gateway from pool
				if server != "" {
					g.ClientMutex.Lock()
					conn := g.Clients[server]
					if conn != nil {
						conn.Close()
						delete(g.Clients, server)
					}
					g.ClientMutex.Unlock()
				}

				Metrics.registeredGatewaysTotal.Dec()
			}
		}

		g.Mutex.Unlock()
	}
}

func (g *RegistryState) getAllConnections() []*grpc.ClientConn {
	connections := make([]*grpc.ClientConn, 0)
	g.Mutex.RLock()
	defer g.Mutex.RUnlock()

	for _, address := range g.Gateways {
		g.ClientMutex.RLock()
		conn, exists := g.Clients[address]
		g.ClientMutex.RUnlock()

		if !exists || conn == nil {
			// TODO: skip instead of creating new connection?
			conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
			if err != nil {
				continue
			}

			g.ClientMutex.Lock()
			if newConn, exists := g.Clients[address]; !exists { // double check to avoid race condition
				g.Clients[address] = conn
				connections = append(connections, conn)
			} else {
				conn.Close()
				connections = append(connections, newConn)
			}
			g.ClientMutex.Unlock()
		} else {
			connections = append(connections, conn)
		}
	}

	return connections
}
