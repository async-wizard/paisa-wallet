package obs

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"time"
)

type routeKey struct{}

type route struct{ pattern string }

// Route tags a handler with its route pattern, so logs group requests by
// "GET /wallets/{id}" rather than by every distinct wallet id.
func Route(pattern string, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rt, ok := r.Context().Value(routeKey{}).(*route); ok {
			rt.pattern = pattern
		}
		h.ServeHTTP(w, r)
	})
}

// quietRoutes are measured but left out of the access log: probes and scrapes would
// otherwise drown out the requests worth reading.
var quietRoutes = map[string]bool{
	"GET /healthz":     true,
	"GET /metrics":     true,
	"GET /logs/stream": true,
}

// Instrument records request metrics and writes one access-log line per request.
func Instrument(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rt := &route{pattern: "unmatched"}
		ctx := context.WithValue(r.Context(), routeKey{}, rt)
		rec := &statusRecorder{ResponseWriter: w}
		start := time.Now()

		next.ServeHTTP(rec, r.WithContext(ctx))
		elapsed := time.Since(start)

		HTTPRequests.WithLabelValues(rt.pattern, strconv.Itoa(rec.Status())).Inc()
		HTTPDuration.WithLabelValues(rt.pattern).Observe(elapsed.Seconds())

		if quietRoutes[rt.pattern] {
			return
		}
		slog.InfoContext(ctx, "http.request",
			"event", "http.request",
			"method", r.Method,
			"route", rt.pattern,
			"path", r.URL.Path,
			"status", rec.Status(),
			"duration_ms", float64(elapsed.Microseconds())/1000,
			"bytes", rec.bytes,
		)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

func (s *statusRecorder) Status() int {
	if s.status == 0 {
		return http.StatusOK
	}
	return s.status
}

// Flush and Unwrap keep streaming responses working through the wrapper.
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *statusRecorder) Unwrap() http.ResponseWriter {
	return s.ResponseWriter
}
