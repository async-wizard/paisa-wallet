package api

import (
	"net/http"

	"github.com/async-wizard/paisa-wallet/internal/obs"
	"github.com/async-wizard/paisa-wallet/internal/wallet"
)

func NewRouter(svc *wallet.Service, adminToken string, logs *obs.Stream) http.Handler {
	h := &handler{svc: svc, adminToken: adminToken}
	mux := http.NewServeMux()
	handle := func(pattern string, fn http.HandlerFunc) {
		mux.Handle(pattern, obs.Route(pattern, fn))
	}

	handle("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})
	handle("GET /metrics", obs.MetricsHandler().ServeHTTP)
	handle("GET /internal/invariants", h.invariants)
	handle("GET /logs/stream", logs.ServeHTTP)

	handle("POST /wallets", h.requireUser(h.createWallet))
	handle("GET /wallets/{id}", h.requireUser(h.getWallet))
	handle("POST /wallets/{id}/credit", h.creditWallet)

	handle("POST /transfers", h.requireUser(h.createTransfer))
	handle("GET /transfers/{id}", h.requireUser(h.getTransfer))

	return withRequestID(obs.Instrument(mux))
}
