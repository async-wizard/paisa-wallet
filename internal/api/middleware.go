package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/async-wizard/paisa-wallet/internal/wallet"
	"github.com/google/uuid"
)

const requestIDHeader = "X-Request-Id"

type (
	requestIDKey struct{}
	userIDKey    struct{}
)

type handler struct {
	svc        *wallet.Service
	adminToken string
}

func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(requestIDHeader)
		if !validRequestID(id) {
			id = uuid.NewString()
		}
		w.Header().Set(requestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id)))
	})
}

func validRequestID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

func requestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

func (h *handler) requireUser(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r)
		if !ok {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeError(w, http.StatusUnauthorized, "unauthorized", "missing or malformed bearer token")
			return
		}

		sum := sha256.Sum256([]byte(token))
		userID, err := h.svc.UserIDForToken(r.Context(), hex.EncodeToString(sum[:]))
		if err != nil {
			serverError(w, r, err)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), userIDKey{}, userID)))
	}
}

func bearerToken(r *http.Request) (string, bool) {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	if token == "" || len(token) > 256 {
		return "", false
	}
	for _, c := range token {
		if c <= ' ' || c > '~' {
			return "", false
		}
	}
	return token, true
}

func userID(ctx context.Context) uuid.UUID {
	id, _ := ctx.Value(userIDKey{}).(uuid.UUID)
	return id
}
