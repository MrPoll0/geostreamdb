module gateway

go 1.25.4

require github.com/go-chi/chi/v5 v5.2.3

require (
	geostreamdb/proto v0.0.0
	github.com/google/uuid v1.6.0
	github.com/mmcloughlin/geohash v0.10.0
	github.com/zeebo/xxh3 v1.0.2
	google.golang.org/grpc v1.77.0
)

require (
	github.com/klauspost/cpuid/v2 v2.0.9 // indirect
	golang.org/x/net v0.47.0 // indirect
	golang.org/x/sys v0.38.0 // indirect
	golang.org/x/text v0.31.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20251022142026-3a174f9686a8 // indirect
	google.golang.org/protobuf v1.36.10 // indirect
)

replace geostreamdb/proto => ../proto
