package main

import (
	"hash/crc32"
	"log"
	"sort"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

var state = &GatewayState{
	ring:    make(HashRing, 0),
	clients: make(map[string]*grpc.ClientConn),
	nodes:   make(map[uint32]bool),
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
	nodes       map[uint32]bool
	clients     map[string]*grpc.ClientConn
	clientMutex sync.RWMutex
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

func (g *GatewayState) addNode(workerId string, address string) {
	hash := crc32.ChecksumIEEE([]byte(workerId))
	node := RingNode{Hash: hash, Server: address}

	g.ringMutex.Lock()
	defer g.ringMutex.Unlock()

	if _, exists := g.nodes[hash]; exists {
		return
	}
	g.ring = append(g.ring, node)
	g.nodes[hash] = true
	sort.Sort(g.ring)
	log.Printf("Added worker %s at %s to the ring", workerId, address)
}
