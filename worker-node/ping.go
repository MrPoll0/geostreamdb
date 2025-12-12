package main

import (
	"context"
	pb "geostreamdb/proto"
	"log"
	"sync"
	"time"
)

type TimeBufferSlot struct {
	Mutex sync.RWMutex // Each TTL time slot has its own mutex to allow parallel access
	Data  *TimeBufferElement
}

type TrieNode struct {
	Children map[byte]*TrieNode // character (byte representation) -> child node
	Count    int64
}

type TimeBufferElement struct {
	Timestamp int64
	TrieRoot  *TrieNode
}

var (
	PING_TTL int64 = 10 // seconds

	timeBuffer = make([]*TimeBufferSlot, PING_TTL)
)

func init() { // runs automatically before main()
	// for the mutexes to exist
	for i := 0; i < int(PING_TTL); i++ {
		timeBuffer[i] = &TimeBufferSlot{}
	}
}

func (t *TrieNode) Increment(geohash string) {
	t.Count++ // increment the whole

	current := t
	for i := 0; i < len(geohash); i++ {
		if current.Children == nil {
			current.Children = make(map[byte]*TrieNode)
		}

		char := geohash[i]
		child, exists := current.Children[char]
		if !exists {
			child = &TrieNode{Count: 0}
			current.Children[char] = child
		}

		child.Count++
		current = child
	}
}

func (t *TrieNode) GetCount(geohash string) int64 {
	if t == nil {
		return 0
	}

	current := t
	for i := 0; i < len(geohash); i++ {
		if current.Children == nil {
			return 0
		}

		char := geohash[i]
		child, exists := current.Children[char]
		if !exists {
			return 0
		}

		current = child
	}

	return current.Count
}

func cleanupTimeBuffer() {
	interval := (5 * PING_TTL) / 2
	ticker := time.NewTicker(time.Duration(interval) * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		now := time.Now().Unix()
		cutoff := now - PING_TTL

		// check all slots for stale data (older than cutoff)
		for i := 0; i < int(PING_TTL); i++ {
			slot := timeBuffer[i]

			slot.Mutex.Lock()
			if slot.Data != nil && slot.Data.Timestamp < cutoff {
				// remove the stale slot. GC will handle the rest
				slot.Data = nil
				log.Printf("removed stale slot at index %d", i)
			}
			slot.Mutex.Unlock()
		}
	}
}

type grpcServer struct {
	pb.UnimplementedWorkerServer
}

func (s *grpcServer) SendPing(ctx context.Context, req *pb.PingRequest) (*pb.PingResponse, error) {
	log.Printf("Received ping request for geohash: %s", req.Geohash)

	now := time.Now().Unix()
	idx := int(now % PING_TTL)
	slot := timeBuffer[idx]

	slot.Mutex.Lock()
	defer slot.Mutex.Unlock()

	// (re)initialize buffer element if nil or expired
	if slot.Data == nil || (slot.Data.Timestamp != now) {
		slot.Data = &TimeBufferElement{
			Timestamp: now,
			TrieRoot:  &TrieNode{Count: 0}, // IncrementTrie will initialize the children map if nil
		}
	}

	slot.Data.TrieRoot.Increment(req.Geohash)

	return &pb.PingResponse{Success: true}, nil
}

func (s *grpcServer) GetPings(ctx context.Context, req *pb.GetPingsRequest) (*pb.GetPingsResponse, error) {
	log.Printf("Received get pings request")

	now := time.Now().Unix()
	cutoff := now - PING_TTL
	total := int64(0)

	for i := 0; i < int(PING_TTL); i++ {
		slot := timeBuffer[i]

		slot.Mutex.RLock()

		// avoid stale/nil data
		if slot.Data != nil && slot.Data.Timestamp >= cutoff {
			total += slot.Data.TrieRoot.GetCount(req.Geohash)
		}

		slot.Mutex.RUnlock()
	}

	return &pb.GetPingsResponse{Count: total, Timestamp: now}, nil
}
