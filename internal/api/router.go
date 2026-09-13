package api

import (
	"net/http"

	"github.com/async-wizard/paisa-wallet/internal/wallet"
)

func NewRouter(svc *wallet.Service) http.Handler {
	h := &handler{svc: svc}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})

	mux.HandleFunc("POST /wallets", h.requireUser(h.createWallet))
	mux.HandleFunc("GET /wallets/{id}", h.requireUser(h.getWallet))

	return withRequestID(mux)
}
