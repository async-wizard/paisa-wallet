package api

import (
	"errors"
	"net/http"

	"github.com/async-wizard/paisa-wallet/internal/wallet"
	"github.com/google/uuid"
)

func (h *handler) createWallet(w http.ResponseWriter, r *http.Request) {
	wal, created, err := h.svc.GetOrCreateWallet(r.Context(), userID(r.Context()))
	if err != nil {
		serverError(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, wal)
}

func (h *handler) getWallet(w http.ResponseWriter, r *http.Request) {
	walletID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "wallet_not_found", "wallet not found")
		return
	}

	wal, err := h.svc.GetWallet(r.Context(), userID(r.Context()), walletID)
	if errors.Is(err, wallet.ErrNotFound) {
		writeError(w, http.StatusNotFound, "wallet_not_found", "wallet not found")
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, wal)
}
