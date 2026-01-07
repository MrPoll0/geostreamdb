package main

import (
	"context"
	pb "geostreamdb/proto"
	"sort"
	"sync"
	"time"
)

const SHARDING_PRECISION = 6 // TODO: make configurable and shared with gateway

type ghBbox struct {
	minLat float64
	maxLat float64
	minLng float64
	maxLng float64
}

func (a ghBbox) intersects(b ghBbox) bool {
	// strict overlap (max bounds are exclusive) to avoid counting boxes that only touch at an edge.
	return a.minLat < b.maxLat && a.maxLat > b.minLat && a.minLng < b.maxLng && a.maxLng > b.minLng
}

var geohashBase32 = "0123456789bcdefghjkmnpqrstuvwxyz"

func geohashDecodeBbox(gh string) (ghBbox, bool) {
	if gh == "" {
		return ghBbox{}, false
	}

	var charmap [256]byte
	for i := range charmap {
		charmap[i] = 0xFF
	}
	for i := 0; i < len(geohashBase32); i++ {
		charmap[geohashBase32[i]] = byte(i)
	}

	minLat, maxLat := -90.0, 90.0
	minLng, maxLng := -180.0, 180.0
	isLng := true // geohash bits start with longitude

	for i := 0; i < len(gh); i++ {
		c := gh[i]
		// normalize ASCII uppercase -> lowercase (geohash alphabet is lowercase)
		if c >= 'A' && c <= 'Z' {
			c = c + ('a' - 'A')
		}
		v := charmap[c] // base32 char -> [0, 31]
		if v == 0xFF {
			return ghBbox{}, false
		}
		for bit := 4; bit >= 0; bit-- { // 5 bits per geohash char
			mask := byte(1 << uint(bit))
			if isLng {
				mid := (minLng + maxLng) / 2
				if v&mask != 0 {
					minLng = mid
				} else {
					maxLng = mid
				}
			} else {
				mid := (minLat + maxLat) / 2
				if v&mask != 0 {
					minLat = mid
				} else {
					maxLat = mid
				}
			}
			isLng = !isLng
		}
	}

	return ghBbox{minLat: minLat, maxLat: maxLat, minLng: minLng, maxLng: maxLng}, true
}

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

func (t *TrieNode) GetAreaCount(precision int32, aggPrecision int32, minLat float64, maxLat float64, minLng float64, maxLng float64, geohashes []string) map[string]int64 {
	if t == nil {
		return nil
	}
	if precision < 1 || aggPrecision < 1 {
		return nil
	}
	if len(geohashes) == 0 {
		return nil
	}

	queryBbox := ghBbox{minLat: minLat, maxLat: maxLat, minLng: minLng, maxLng: maxLng}
	counts := make(map[string]int64)

	for _, geohash := range geohashes {
		if len(geohash) < int(aggPrecision) {
			continue
		}

		// traverse from root to the aggregated geohash node
		current := t
		for i := 0; i < int(aggPrecision); i++ {
			if current.Children == nil {
				current = nil
				break
			}
			child, exists := current.Children[geohash[i]]
			if !exists {
				current = nil
				break
			}
			current = child
		}
		if current == nil {
			continue
		}

		// find all (max 32) leaf nodes for the next precision level, and so on for every node found until desired precision is reached
		// filter out nodes that are not within the bounding box

		// if the requested precision is coarser than (or equal to) the aggregated precision,
		// aggregate counts from the covered aggPrecision cells into the coarser prefix
		if precision <= aggPrecision {
			prefix := geohash[:precision]
			// only count traffic from the covered (aggPrecision) cells, otherwise we'd
			// (a) include pings outside the bbox but within the same coarse prefix, and
			// (b) double count by adding the same coarse prefix total once per covered cell (looping through all covered cells)
			aggCellGh := geohash[:aggPrecision]
			cell, ok := geohashDecodeBbox(aggCellGh)
			if !ok || !cell.intersects(queryBbox) {
				continue
			}
			counts[prefix] += t.GetCount(aggCellGh)
			continue
		}

		// find all leaf nodes at the desired precision via DFS
		type stackItem struct {
			node   *TrieNode
			prefix string
			depth  int32
		}

		stack := []stackItem{{node: current, prefix: geohash, depth: aggPrecision}}
		for len(stack) > 0 {
			n := stack[len(stack)-1]
			stack = stack[:len(stack)-1]

			if n.node == nil {
				continue
			}

			if n.depth == precision {
				cell, ok := geohashDecodeBbox(n.prefix)
				if ok && cell.intersects(queryBbox) {
					counts[n.prefix] += n.node.Count
				}
				continue
			}

			if n.node.Children == nil {
				continue
			}

			nextDepth := n.depth + 1
			for ch, child := range n.node.Children {
				nextPrefix := n.prefix + string(ch)
				cell, ok := geohashDecodeBbox(nextPrefix)
				if !ok || !cell.intersects(queryBbox) {
					continue
				}
				stack = append(stack, stackItem{node: child, prefix: nextPrefix, depth: nextDepth})
			}
		}
	}

	return counts
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
				// log.Printf("removed stale slot at index %d", i)
			}
			slot.Mutex.Unlock()
		}
	}
}

func observeGRPC(method string, err error, start time.Time) {
	result := "success"
	if err != nil {
		result = "failure"
	}
	Metrics.gRPCRequestsTotal.WithLabelValues(method, result).Inc()
	Metrics.gRPCLatency.WithLabelValues(method).Observe(time.Since(start).Seconds())
}

type grpcServer struct {
	pb.UnimplementedWorkerServer
}

func (s *grpcServer) SendPing(ctx context.Context, req *pb.PingRequest) (*pb.PingResponse, error) {
	//log.Printf("Received ping request for geohash: %s", req.Geohash)

	start := time.Now()
	var err error // for error handling, not implemented yet
	defer func() {
		observeGRPC("SendPing", err, start)
	}()

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

	// track pings stored per geohash prefix (at sharding precision)
	// TTL must be taken into account externally
	ghPrefix := req.Geohash
	if len(ghPrefix) > SHARDING_PRECISION {
		ghPrefix = ghPrefix[:SHARDING_PRECISION]
	}
	Metrics.pingsStoredTotal.WithLabelValues(ghPrefix).Inc()

	return &pb.PingResponse{Success: true}, nil
}

func (s *grpcServer) GetPings(ctx context.Context, req *pb.GetPingsRequest) (*pb.GetPingsResponse, error) {
	//log.Printf("Received get pings request")

	start := time.Now()
	var err error // for error handling, not implemented yet
	defer func() {
		observeGRPC("GetPings", err, start)
	}()

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

func (s *grpcServer) GetPingArea(ctx context.Context, req *pb.GetPingAreaRequest) (*pb.GetPingAreaResponse, error) {
	start := time.Now()
	var err error // for error handling, not implemented yet
	defer func() {
		observeGRPC("GetPingArea", err, start)
	}()

	now := time.Now().Unix()
	cutoff := now - PING_TTL
	combined := make(map[string]int64)

	for i := 0; i < int(PING_TTL); i++ {
		slot := timeBuffer[i]

		slot.Mutex.RLock()

		// avoid stale/nil data
		if slot.Data != nil && slot.Data.Timestamp >= cutoff && slot.Data.TrieRoot != nil {
			m := slot.Data.TrieRoot.GetAreaCount(req.Precision, req.AggPrecision, req.MinLat, req.MaxLat, req.MinLng, req.MaxLng, req.Geohashes)
			for gh, c := range m {
				combined[gh] += c
			}
		}

		slot.Mutex.RUnlock()
	}

	// convert combined map to response format
	keys := make([]string, 0, len(combined))
	for gh := range combined {
		keys = append(keys, gh)
	}
	sort.Strings(keys)

	out := make([]*pb.PingAreaCount, 0, len(keys))
	for _, gh := range keys {
		out = append(out, &pb.PingAreaCount{Geohash: gh, Count: combined[gh]})
	}

	return &pb.GetPingAreaResponse{Counts: out}, nil
}
