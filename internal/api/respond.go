package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"

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
	slog.ErrorContext(r.Context(), "request failed", "err", err.Error())

	if databaseUnavailable(err) {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "service temporarily unavailable")
		return
	}
	writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
}

// databaseUnavailable reports failures caused by losing the database rather than by a bug.
// These get 503: the request was refused, not mishandled. If a connection drops mid-commit
// the outcome is unknown to us, and retrying with the same idempotency key is exactly how
// the client finds out safely.
func databaseUnavailable(err error) bool {
	var (
		connErr *pgconn.ConnectError
		pgErr   *pgconn.PgError
	)
	switch {
	case errors.As(err, &connErr),
		errors.Is(err, context.DeadlineExceeded),
		errors.Is(err, io.ErrUnexpectedEOF),
		pgconn.Timeout(err):
		return true
	case errors.As(err, &pgErr):
		// Class 08: connection exception. Class 57P: server shutting down or not accepting
		// connections (e.g. 57P01 admin_shutdown during a restart or failover).
		return strings.HasPrefix(pgErr.Code, "08") || strings.HasPrefix(pgErr.Code, "57P")
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}
