package main

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

type metrics struct {
	registeredGatewaysTotal prometheus.Gauge
	gRPCRequestsTotal       *prometheus.CounterVec   // per method and result (success/failure)
	gRPCLatency             *prometheus.HistogramVec // per method
}

var Metrics = metrics{
	registeredGatewaysTotal: promauto.NewGauge(prometheus.GaugeOpts{
		Name: "registry_registered_gateways_total",
		Help: "Total count of registered gateways",
	}),
	gRPCRequestsTotal: promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "registry_grpc_requests_total",
		Help: "Total count of gRPC requests by method and result (success/failure)",
	}, []string{"method", "result"}),
	gRPCLatency: promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "registry_grpc_request_duration_seconds",
		Help:    "gRPC request latency in seconds by method",
		Buckets: prometheus.DefBuckets,
	}, []string{"method"}),
}
