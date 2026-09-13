package api

import (
	"crypto/subtle"
	"net/http"

	"github.com/google/uuid"
)

type creditRequest struct {
	AmountPaise    int64  `json:"amount_paise"`
	IdempotencyKey string `json:"idempotency_key"`
}

// creditWallet is the admin-only faucet: it mints money from the treasury into a wallet.
func (h *handler) creditWallet(w http.ResponseWriter, r *http.Request) {
	token, ok := bearerToken(r)
	if !ok || subtle.ConstantTimeCompare([]byte(token), []byte(h.adminToken)) != 1 {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeError(w, http.StatusUnauthorized, "unauthorized", "admin token required")
		return
	}

	walletID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "wallet_not_found", "wallet not found")
		return
	}

	var req creditRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	out, err := h.svc.Mint(r.Context(), walletID, req.AmountPaise, req.IdempotencyKey)
	if err != nil {
		moneyError(w, r, err)
		return
	}
	writeOutcome(w, out)
}
