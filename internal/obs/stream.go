package obs

import (
	"bytes"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Stream keeps the most recent log lines in memory and fans new ones out to Server-Sent
// Events subscribers, so logs can be watched live at /logs/stream.
type Stream struct {
	mu   sync.Mutex
	ring [][]byte
	next int
	subs map[chan []byte]struct{}
}

const (
	streamBuffer         = 1000
	streamMaxSubscribers = 20
	// Under Cloud Run's default 300s request timeout; clients reconnect to keep watching.
	streamMaxDuration = 4 * time.Minute
)

func NewStream() *Stream {
	return &Stream{ring: make([][]byte, 0, streamBuffer), subs: map[chan []byte]struct{}{}}
}

// Write receives one JSON log line per call from the slog handler. It never blocks on a
// subscriber: a reader that falls behind misses lines rather than slowing requests down.
func (s *Stream) Write(p []byte) (int, error) {
	line := bytes.TrimRight(bytes.Clone(p), "\n")

	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.ring) < streamBuffer {
		s.ring = append(s.ring, line)
	} else {
		s.ring[s.next] = line
		s.next = (s.next + 1) % streamBuffer
	}
	for ch := range s.subs {
		select {
		case ch <- line:
		default:
		}
	}
	return len(p), nil
}

// ServeHTTP sends the buffered log lines, then new ones as they are written.
func (s *Stream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	ch := make(chan []byte, 256)
	s.mu.Lock()
	if len(s.subs) >= streamMaxSubscribers {
		s.mu.Unlock()
		http.Error(w, "too many log stream subscribers", http.StatusServiceUnavailable)
		return
	}
	backlog := make([][]byte, 0, len(s.ring))
	for i := range s.ring {
		backlog = append(backlog, s.ring[(s.next+i)%len(s.ring)])
	}
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.mu.Unlock()
	}()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	for _, line := range backlog {
		fmt.Fprintf(w, "data: %s\n\n", line)
	}
	flusher.Flush()

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	deadline := time.NewTimer(streamMaxDuration)
	defer deadline.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-deadline.C:
			return
		case <-heartbeat.C:
			fmt.Fprint(w, ": keep-alive\n\n")
			flusher.Flush()
		case line := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", line)
			flusher.Flush()
		}
	}
}
