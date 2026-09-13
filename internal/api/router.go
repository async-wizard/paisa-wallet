package api

import (
	"net/http"

	"github.com/async-wizard/paisa-wallet/internal/wallet"
)

func NewRouter(svc *wallet.Service, adminToken string) http.Handler {
	h := &handler{svc: svc, adminToken: adminToken}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})

	mux.HandleFunc("POST /wallets", h.requireUser(h.createWallet))
	mux.HandleFunc("GET /wallets/{id}", h.requireUser(h.getWallet))
	mux.HandleFunc("POST /wallets/{id}/credit", h.creditWallet)

	mux.HandleFunc("POST /transfers", h.requireUser(h.createTransfer))
	mux.HandleFunc("GET /transfers/{id}", h.requireUser(h.getTransfer))

	return withRequestID(mux)
}
