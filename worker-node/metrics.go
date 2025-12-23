package main

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

type metrics struct {
	pingsStoredTotal  *prometheus.CounterVec   // per geohash prefix at sharding precision (TTL must be taken into account externally)
	gRPCRequestsTotal *prometheus.CounterVec   // per method and result (success/failure)
	gRPCLatency       *prometheus.HistogramVec // per method
}

var Metrics = metrics{
	pingsStoredTotal: promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "worker_pings_stored_total",
		Help: "Total count of pings stored at sharding precision by geohash prefix (TTL must be taken into account)",
	}, []string{"gh_prefix"}),
	gRPCRequestsTotal: promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "worker_grpc_requests_total",
		Help: "Total count of gRPC requests by method and result (success/failure)",
	}, []string{"method", "result"}),
	gRPCLatency: promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "worker_grpc_request_duration_seconds",
		Help:    "gRPC request latency in seconds by method",
		Buckets: prometheus.DefBuckets,
	}, []string{"method"}),
}
