package main

import (
	"context"
	"encoding/json"
	"hash/crc32"
	"log"
	"net"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/mmcloughlin/geohash"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pb "hello_go/proto"
)

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

var state = &GatewayState{
	ring:    make(HashRing, 0),
	clients: make(map[string]*grpc.ClientConn),
	nodes:   make(map[uint32]bool),
}

type gpsPing struct {
	Latitude  float64 `json:"lat"`
	Longitude float64 `json:"lng"`
}

func setup_router() *chi.Mux {
	router := chi.NewRouter()
	router.Use(middleware.Logger)

	// router.Get("/ping", getPings)
	router.Post("/ping", postPing)

	return router
}

type grpcServer struct {
	pb.UnimplementedGatewayServer
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

func (s *grpcServer) Heartbeat(ctx context.Context, req *pb.HeartbeatRequest) (*pb.HeartbeatResponse, error) {
	state.addNode(req.WorkerId, req.Address)
	return &pb.HeartbeatResponse{Acknowledged: true}, nil
}

func setup_heartbeat_listener() {
	port := os.Getenv("HEARTBEAT_PORT")
	if port == "" {
		port = "50051"
	}
	lis, err := net.Listen("tcp", ":"+port)
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}

	s := grpc.NewServer()
	pb.RegisterGatewayServer(s, &grpcServer{})
	log.Printf("grpc server listening at %v", lis.Addr())
	if err := s.Serve(lis); err != nil {
		log.Fatalf("failed to serve: %v", err)
	}
}

func main() {
	go setup_heartbeat_listener()

	router := setup_router()

	httpPort := os.Getenv("PORT")
	if httpPort == "" {
		httpPort = "8080"
	}
	log.Printf("HTTP server listening on port %s", httpPort)
	if err := http.ListenAndServe(":"+httpPort, router); err != nil {
		log.Fatalf("failed to serve: %v", err)
	}
}

func postPing(w http.ResponseWriter, r *http.Request) {
	var newGpsPing gpsPing

	if err := json.NewDecoder(r.Body).Decode(&newGpsPing); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Invalid request body"))
		return
	}

	gh := geohash.Encode(newGpsPing.Latitude, newGpsPing.Longitude)
	// truncate geohash for better locality
	if len(gh) > 7 {
		gh = gh[:7]
	}

	// get the address of the worker node responsible for this geohash
	targetAddr := state.GetNodeAddress(gh)
	if targetAddr == "" {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("No workers available"))
		return
	}

	// get a connection to the worker node (pool of connections, do not close)
	conn, err := state.GetConn(targetAddr)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Failed to connect to worker"))
		return
	}

	client := pb.NewWorkerClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, err = client.SendPing(ctx, &pb.PingRequest{Geohash: gh})
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Failed to contact worker"))
		return
	}

	w.WriteHeader(http.StatusCreated)
	w.Write([]byte("Ping sent, geohash: " + gh))
}

/*func getPings(w http.ResponseWriter, _ *http.Request) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	v, err := client.GetPings(ctx, &pb.GetPingsRequest{})
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Failed to get pings"))
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(v.Pings)
}*/
