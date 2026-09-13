package obs

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Registry holds every metric the service exposes on /metrics.
var Registry = prometheus.NewRegistry()

var (
	HTTPRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "paisa_http_requests_total",
		Help: "HTTP requests by route pattern and status code.",
	}, []string{"route", "code"})

	HTTPDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "paisa_http_request_duration_seconds",
		Help:    "HTTP request latency by route pattern.",
		Buckets: []float64{.001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5},
	}, []string{"route"})

	TransfersCreated = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "paisa_transfers_created_total",
		Help: "Transfers executed for the first time (not replays), whatever the outcome.",
	}, []string{"kind"})

	TransfersDeclined = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "paisa_transfers_declined_total",
		Help: "Transfers declined without moving money, by reason.",
	}, []string{"kind", "reason"})

	IdempotentReplays = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "paisa_idempotent_replays_total",
		Help: "Requests answered from a stored response because their idempotency key was already used.",
	}, []string{"kind"})
)

func init() {
	Registry.MustRegister(HTTPRequests, HTTPDuration, TransfersCreated, TransfersDeclined, IdempotentReplays)

	// Create every series up front, so a counter reads 0 rather than being absent until
	// its first event.
	for _, kind := range []string{"transfer", "mint"} {
		TransfersCreated.WithLabelValues(kind)
		TransfersDeclined.WithLabelValues(kind, "insufficient_funds")
		IdempotentReplays.WithLabelValues(kind)
	}
}

func MetricsHandler() http.Handler {
	return promhttp.HandlerFor(Registry, promhttp.HandlerOpts{})
}
