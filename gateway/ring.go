package main

import (
	"hash/crc32"
	"log"
	"sort"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

var state = &GatewayState{
	ring:     make(HashRing, 0),
	clients:  make(map[string]*grpc.ClientConn),
	lastSeen: make(map[uint32]int64),
}

type RingNode struct {
	Hash   uint32
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
	lastSeen    map[uint32]int64            // hash -> last seen timestamp
	clients     map[string]*grpc.ClientConn // address -> grpc client connection
	clientMutex sync.RWMutex
}

func (g *GatewayState) addNode(workerId string, address string) {
	hash := crc32.ChecksumIEEE([]byte(workerId))
	node := RingNode{Hash: hash, Server: address}

	g.ringMutex.Lock()
	defer g.ringMutex.Unlock()

	now := time.Now().Unix()
	if _, exists := g.lastSeen[hash]; exists {
		g.lastSeen[hash] = now // update last seen timestamp
		return
	}

	g.ring = append(g.ring, node)
	g.lastSeen[hash] = now
	sort.Sort(g.ring)
	log.Printf("Added worker %s at %s to the ring", workerId, address)
}

func (g *GatewayState) removeNode(hash uint32) {
	g.ringMutex.Lock()
	defer g.ringMutex.Unlock()

	g.removeNodeLocked(hash)
}

func (g *GatewayState) removeNodeLocked(hash uint32) {
	// no remapping of keys (geohashes) needed because of their short TTL

	// binary search
	index := sort.Search(len(g.ring), func(i int) bool {
		return g.ring[i].Hash >= hash
	})
	if index >= len(g.ring) || g.ring[index].Hash != hash {
		return // not found
	}

	g.ring = append(g.ring[:index], g.ring[index+1:]...)
	delete(g.lastSeen, hash)
	log.Printf("Removed worker from ring")
}

func (g *GatewayState) cleanupDeadNodes(ttl time.Duration) {
	ticker := time.NewTicker(ttl)
	defer ticker.Stop()

	for range ticker.C {
		g.ringMutex.Lock()

		now := time.Now().Unix()
		for hash, lastSeen := range g.lastSeen {
			if now-lastSeen > int64(ttl.Seconds()) {
				g.removeNodeLocked(hash)
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

	hash := crc32.ChecksumIEEE([]byte(geohash))

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
