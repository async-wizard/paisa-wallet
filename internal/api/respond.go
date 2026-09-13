package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgconn"
)

type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorBody{Error: errorDetail{Code: code, Message: message}})
}

func serverError(w http.ResponseWriter, r *http.Request, err error) {
	slog.ErrorContext(r.Context(), "request failed", "request_id", requestID(r.Context()), "err", err)

	var connErr *pgconn.ConnectError
	if errors.As(err, &connErr) || errors.Is(err, context.DeadlineExceeded) {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "service temporarily unavailable")
		return
	}
	writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
}
