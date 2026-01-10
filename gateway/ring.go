package main

import (
	"log"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/zeebo/xxh3"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

var NUM_VIRTUAL_NODES = 256 // per physical node

var state = &GatewayState{
	ring:     make(HashRing, 0),
	clients:  make(map[string]*grpc.ClientConn),
	lastSeen: make(map[string]int64),
}

type RingNode struct {
	Hash   uint64
	Server string
}

type HashRing []RingNode

// methods needed for sorting
func (h HashRing) Len() int {
	return len(h)
}
func (h HashRing) Less(i, j int) bool {
	return h[i].Hash < h[j].Hash
}
func (h HashRing) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
}

type GatewayState struct {
	ringMutex   sync.RWMutex
	ring        HashRing
	lastSeen    map[string]int64            // worker id (vnode-independent) -> last seen timestamp
	clients     map[string]*grpc.ClientConn // address -> grpc client connection
	clientMutex sync.RWMutex
}

func (g *GatewayState) addNode(workerId string, address string) {
	g.ringMutex.Lock() // append all vnodes atomically
	defer g.ringMutex.Unlock()

	// TODO: can addresses change? if same worker (id) sends heartbeat but with different address, that won't be reflected in the ring

	now := time.Now().Unix()
	// check if physical node already in the ring
	if _, exists := g.lastSeen[workerId]; exists {
		g.lastSeen[workerId] = now // update last seen timestamp
		return
	}

	// New node added - increment metric
	Metrics.workerNodesTotal.Inc()

	for i := 0; i < NUM_VIRTUAL_NODES; i++ {
		id := workerId + "#" + strconv.Itoa(i)
		hash := xxh3.HashString(id)
		node := RingNode{Hash: hash, Server: address}

		g.ring = append(g.ring, node)
		log.Printf("Added worker %s at %s to the ring", id, address)
	}

	sort.Sort(g.ring)
	g.lastSeen[workerId] = now
}

func (g *GatewayState) removeNode(workerId string) {
	g.ringMutex.Lock()
	defer g.ringMutex.Unlock()

	g.removeNodeLocked(workerId)
}

func (g *GatewayState) removeNodeLocked(workerId string) string {
	// removes a physical node along all its virtual nodes
	// no remapping of keys (geohashes) needed because of their short TTL
	server := ""
	for i := 0; i < NUM_VIRTUAL_NODES; i++ {
		id := workerId + "#" + strconv.Itoa(i)
		hash := xxh3.HashString(id)

		// binary search
		index := sort.Search(len(g.ring), func(j int) bool {
			return g.ring[j].Hash >= hash
		})

		if index >= len(g.ring) || g.ring[index].Hash != hash {
			continue
		}

		server = g.ring[index].Server
		g.ring = append(g.ring[:index], g.ring[index+1:]...)

		log.Printf("Removed worker %s from ring", server)
	}

	delete(g.lastSeen, workerId)

	if server != "" {
		Metrics.workerNodesTotal.Dec()
	}

	return server
}

func (g *GatewayState) cleanupDeadNodes(ttl time.Duration, tick_time time.Duration) {
	ticker := time.NewTicker(tick_time)
	defer ticker.Stop()

	for range ticker.C {
		g.ringMutex.Lock()

		now := time.Now().Unix()
		for workerId, lastSeen := range g.lastSeen {
			if now-lastSeen > int64(ttl.Seconds()) {
				// remove node from ring
				server := g.removeNodeLocked(workerId)
				// close and delete connection to worker node from pool
				if server != "" {
					g.clientMutex.Lock()
					conn := g.clients[server]
					if conn != nil {
						conn.Close()
						delete(g.clients, server)
					}
					g.clientMutex.Unlock()
				}
			}
		}

		g.ringMutex.Unlock()
	}
}

func (g *GatewayState) GetConn(address string) (*grpc.ClientConn, error) {
	g.clientMutex.RLock()
	conn, exists := g.clients[address]
	g.clientMutex.RUnlock()

	if exists {
		return conn, nil
	}

	g.clientMutex.Lock()
	defer g.clientMutex.Unlock()

	// double check
	if conn, exists := g.clients[address]; exists {
		return conn, nil
	}

	newConn, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Printf("failed to create new client connection: %v", err)
		return nil, err
	}

	g.clients[address] = newConn
	return newConn, nil
}

func (g *GatewayState) GetNodeAddress(geohash string) string {
	g.ringMutex.RLock()
	defer g.ringMutex.RUnlock()

	if len(g.ring) == 0 {
		return ""
	}

	hash := xxh3.HashString(geohash)

	// binary search O(log n)
	index := sort.Search(len(g.ring), func(i int) bool {
		return g.ring[i].Hash >= hash
	})
	// wrap around
	if index == len(g.ring) {
		index = 0
	}

	return g.ring[index].Server
}
