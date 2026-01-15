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
// TODO: implement power of two choices of consistent hashing with bounded loads to improve distribution even further (but with added costs)

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

	// new node added: increment metric
	Metrics.workerNodesTotal.Inc()

	// pre-allocate capacity to avoid reallocs during append
	if cap(g.ring)-len(g.ring) < NUM_VIRTUAL_NODES {
		// current capacity is not enough, allocate a new one
		newRing := make(HashRing, len(g.ring), len(g.ring)+NUM_VIRTUAL_NODES)
		copy(newRing, g.ring)
		g.ring = newRing
	}

	// reuse buffer for string building (avoids alloc per iteration)
	var buf []byte
	for i := 0; i < NUM_VIRTUAL_NODES; i++ {
		buf = buf[:0]                  // reset buffer
		buf = append(buf, workerId...) // unpack workerId string into bytes and append
		buf = append(buf, '#')
		buf = strconv.AppendInt(buf, int64(i), 10)

		hash := xxh3.HashString(string(buf))
		g.ring = append(g.ring, RingNode{Hash: hash, Server: address})
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

	// collect all hashes to remove first (avoid modifying slice while iterating)
	hashesToRemove := make(map[uint64]struct{}, NUM_VIRTUAL_NODES)
	var buf []byte // reuse buffer for string building
	for i := 0; i < NUM_VIRTUAL_NODES; i++ {
		buf = buf[:0]
		buf = append(buf, workerId...)
		buf = append(buf, '#')
		buf = strconv.AppendInt(buf, int64(i), 10)
		hash := xxh3.HashString(string(buf))
		hashesToRemove[hash] = struct{}{}
	}

	// single pass: filter out nodes with matching hashes
	server := ""
	newRing := g.ring[:0] // reuse underlying array
	for _, node := range g.ring {
		if _, remove := hashesToRemove[node.Hash]; remove {
			server = node.Server
			continue // skip this node
		}
		newRing = append(newRing, node)
	}
	g.ring = newRing

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
