package main

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

type metrics struct {
	httpRequestsTotal    *prometheus.CounterVec   // per endpoint and status
	httpLatency          *prometheus.HistogramVec // per endpoint
	workerNodesTotal     prometheus.Gauge
	gRPCRequestsTotal    *prometheus.CounterVec   // per worker node and result (success/failure)
	gRPCLatency          *prometheus.HistogramVec // per worker node and method
	geohashRequestsTotal *prometheus.CounterVec   // per worker node
}

var Metrics = metrics{
	httpRequestsTotal: promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_http_requests_total",
		Help: "Total count of HTTP requests per endpoint and status code",
	}, []string{"endpoint", "status"}),
	httpLatency: promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "gateway_http_request_duration_seconds",
		Help:    "HTTP request latency in seconds per endpoint",
		Buckets: prometheus.DefBuckets,
	}, []string{"endpoint"}),
	workerNodesTotal: promauto.NewGauge(prometheus.GaugeOpts{
		Name: "gateway_worker_nodes_total",
		Help: "Number of worker nodes",
	}),
	gRPCRequestsTotal: promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_grpc_requests_total",
		Help: "Number of gRPC calls per method, worker node and result (success/failure)",
	}, []string{"method", "result", "worker_node"}),
	gRPCLatency: promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "gateway_grpc_request_duration_seconds",
		Help:    "gRPC request latency in seconds per worker node and method",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "worker_node"}),
	geohashRequestsTotal: promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_geohash_requests_total",
		Help: "Requests routed per worker node and type (routed/broadcast)",
	}, []string{"worker_node", "type"}),
}
