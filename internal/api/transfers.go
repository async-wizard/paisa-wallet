package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/async-wizard/paisa-wallet/internal/wallet"
	"github.com/google/uuid"
)

type transferRequest struct {
	From           uuid.UUID `json:"from"`
	To             uuid.UUID `json:"to"`
	AmountPaise    int64     `json:"amount_paise"`
	IdempotencyKey string    `json:"idempotency_key"`
}

func (h *handler) createTransfer(w http.ResponseWriter, r *http.Request) {
	var req transferRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.From == uuid.Nil || req.To == uuid.Nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "from and to are required wallet ids")
		return
	}

	out, err := h.svc.Transfer(r.Context(), userID(r.Context()), req.From, req.To, req.AmountPaise, req.IdempotencyKey)
	if err != nil {
		moneyError(w, r, err)
		return
	}
	writeOutcome(w, out)
}

func (h *handler) getTransfer(w http.ResponseWriter, r *http.Request) {
	transferID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "transfer_not_found", "transfer not found")
		return
	}

	body, err := h.svc.GetTransfer(r.Context(), userID(r.Context()), transferID)
	if errors.Is(err, wallet.ErrNotFound) {
		writeError(w, http.StatusNotFound, "transfer_not_found", "transfer not found")
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

// decodeJSON reads a single JSON object, rejecting unknown fields so a misspelt
// amount_paise can't silently become zero, and non-integer amounts fail to decode.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body: "+err.Error())
		return false
	}
	if dec.More() {
		writeError(w, http.StatusBadRequest, "invalid_request", "body must contain a single JSON object")
		return false
	}
	return true
}

func writeOutcome(w http.ResponseWriter, out wallet.Outcome) {
	w.Header().Set("Content-Type", "application/json")
	if out.Replayed {
		w.Header().Set("Idempotent-Replay", "true")
	}
	w.WriteHeader(out.StatusCode)
	_, _ = w.Write(out.Body)
}

func moneyError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, wallet.ErrSameWallet):
		writeError(w, http.StatusBadRequest, "same_wallet", "from and to must be different wallets")
	case errors.Is(err, wallet.ErrInvalidAmount):
		writeError(w, http.StatusBadRequest, "invalid_amount", "amount_paise must be a positive integer no greater than 1000000000000")
	case errors.Is(err, wallet.ErrInvalidRequest):
		writeError(w, http.StatusBadRequest, "invalid_request", "idempotency_key is required (1-128 characters)")
	case errors.Is(err, wallet.ErrNotFound):
		writeError(w, http.StatusNotFound, "wallet_not_found", "wallet not found")
	case errors.Is(err, wallet.ErrForbidden):
		writeError(w, http.StatusForbidden, "forbidden", "you can only transfer from your own wallet")
	case errors.Is(err, wallet.ErrKeyReused):
		writeError(w, http.StatusConflict, "idempotency_key_reused", "idempotency_key was already used with a different request")
	default:
		serverError(w, r, err)
	}
}
