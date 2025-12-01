package main

import (
	"context"
	pb "geostreamdb/proto"
	"log"
	"sync"
	"time"
)

type TimePingBucket struct {
	Timestamp int64
	Counts    map[string]int64 // geohash -> count
}

var (
	pingMutex sync.RWMutex

	pingsTotal     = make(map[string]int64)     // geohash -> total count
	timePingBucket = make([]*TimePingBucket, 0) // queue of time-ping buckets

	PING_TTL = 10 // seconds
)

func cleanupTimePingBucket() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		pingMutex.Lock()

		now := time.Now().Unix()
		cutoff := now - int64(PING_TTL)

		for len(timePingBucket) > 0 {
			head := timePingBucket[0]

			// no expired pings
			if head.Timestamp > cutoff {
				break
			}

			// update total count
			for geohash, count := range head.Counts {
				pingsTotal[geohash] -= count

				log.Printf("Removed %d pings from total count for geohash: %s", count, geohash)

				// remove garbage
				if pingsTotal[geohash] <= 0 {
					delete(pingsTotal, geohash)
				}
			}

			// dequeue
			timePingBucket[0] = &TimePingBucket{} // clear memory
			timePingBucket = timePingBucket[1:]
		}

		pingMutex.Unlock()
	}
}

type grpcServer struct {
	pb.UnimplementedWorkerServer
}

func (s *grpcServer) SendPing(ctx context.Context, req *pb.PingRequest) (*pb.PingResponse, error) {
	log.Printf("Received ping request for geohash: %s", req.Geohash)

	pingMutex.Lock()

	// increment total count
	pingsTotal[req.Geohash]++

	// add to time-ping bucket
	now := time.Now().Unix()
	var currentBucket *TimePingBucket
	if len(timePingBucket) > 0 {
		last := len(timePingBucket) - 1
		if timePingBucket[last].Timestamp == now {
			// same timestamp, use existing bucket
			currentBucket = timePingBucket[last]
		}
	}

	if currentBucket == nil {
		// new timestamp, create new bucket
		currentBucket = &TimePingBucket{Timestamp: now, Counts: make(map[string]int64)}
		timePingBucket = append(timePingBucket, currentBucket)
	}

	currentBucket.Counts[req.Geohash]++ // increment count

	log.Printf("Added ping count: %v", pingsTotal)
	pingMutex.Unlock()

	return &pb.PingResponse{Success: true}, nil
}

func (s *grpcServer) GetPings(ctx context.Context, req *pb.GetPingsRequest) (*pb.GetPingsResponse, error) {
	log.Printf("Received get pings request")

	pingMutex.RLock()

	count := pingsTotal[req.Geohash]
	now := time.Now().Unix()

	pingMutex.RUnlock()

	return &pb.GetPingsResponse{Count: count, Timestamp: now}, nil
}
