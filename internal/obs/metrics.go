package obs

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
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

	TransfersSucceeded = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "paisa_transfers_succeeded_total",
		Help: "Transfers that moved money.",
	}, []string{"kind"})

	TransfersDeclined = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "paisa_transfers_declined_total",
		Help: "Transfers declined without moving money, by reason.",
	}, []string{"kind", "reason"})

	IdempotentReplays = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "paisa_idempotent_replays_total",
		Help: "Requests answered from a stored response because their idempotency key was already used.",
	}, []string{"kind"})

	IdempotencyConflicts = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "paisa_idempotency_conflicts_total",
		Help: "Requests rejected because their idempotency key was used with a different body.",
	}, []string{"kind"})

	buildInfo = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "paisa_build_info",
		Help: "Always 1. Labels identify the running version and this process instance, so a reader polling /metrics can tell when it reached a different instance.",
	}, []string{"version", "instance"})

	WalletGetOrCreate = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "paisa_wallet_get_or_create_total",
		Help: "POST /wallets outcomes: created a wallet or returned the existing one.",
	}, []string{"outcome"})
)

func init() {
	Registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		HTTPRequests, HTTPDuration,
		TransfersCreated, TransfersSucceeded, TransfersDeclined,
		IdempotentReplays, IdempotencyConflicts, WalletGetOrCreate, buildInfo,
	)

	// Create every series up front, so a counter reads 0 rather than being absent until
	// its first event.
	for _, kind := range []string{"transfer", "mint"} {
		TransfersCreated.WithLabelValues(kind)
		TransfersSucceeded.WithLabelValues(kind)
		TransfersDeclined.WithLabelValues(kind, "insufficient_funds")
		IdempotentReplays.WithLabelValues(kind)
		IdempotencyConflicts.WithLabelValues(kind)
	}
	WalletGetOrCreate.WithLabelValues("created")
	WalletGetOrCreate.WithLabelValues("existing")
}

// SetBuildInfo records the version and a random id for this process.
func SetBuildInfo(version string) {
	buildInfo.WithLabelValues(version, uuid.NewString()[:8]).Set(1)
}

func MetricsHandler() http.Handler {
	return promhttp.HandlerFor(Registry, promhttp.HandlerOpts{})
}
