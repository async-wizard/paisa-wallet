package obs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Stream keeps the most recent log lines in memory and fans new ones out to Server-Sent
// Events subscribers, so logs can be watched live at /logs/stream. It is per instance: with
// several Cloud Run instances each holds its own buffer.
type Stream struct {
	mu   sync.Mutex
	ring []entry
	next int
	seq  uint64
	subs map[chan entry]struct{}
}

type entry struct {
	seq       uint64
	line      []byte
	event     string
	requestID string
}

const (
	streamBuffer         = 1000
	streamMaxSubscribers = 20
	// Under Cloud Run's default 300s request timeout. The browser's EventSource reconnects
	// with Last-Event-ID and resumes where it left off.
	streamMaxDuration = 4 * time.Minute
)

func NewStream() *Stream {
	return &Stream{ring: make([]entry, 0, streamBuffer), subs: map[chan entry]struct{}{}}
}

// Write receives one JSON log line per call from the slog handler. It never blocks on a
// subscriber: a reader that falls behind misses lines rather than slowing requests down.
func (s *Stream) Write(p []byte) (int, error) {
	var fields struct {
		Event     string `json:"event"`
		RequestID string `json:"request_id"`
	}
	_ = json.Unmarshal(p, &fields)
	line := bytes.TrimRight(bytes.Clone(p), "\n")

	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	e := entry{seq: s.seq, line: line, event: fields.Event, requestID: fields.RequestID}
	if len(s.ring) < streamBuffer {
		s.ring = append(s.ring, e)
	} else {
		s.ring[s.next] = e
		s.next = (s.next + 1) % streamBuffer
	}
	for ch := range s.subs {
		select {
		case ch <- e:
		default:
		}
	}
	return len(p), nil
}

// ServeHTTP streams buffered and live log lines. Optional filters: ?event= (exact event
// name) and ?correlation_id= (a request id).
func (s *Stream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	event := r.URL.Query().Get("event")
	correlationID := r.URL.Query().Get("correlation_id")
	match := func(e entry) bool {
		return (event == "" || e.event == event) && (correlationID == "" || e.requestID == correlationID)
	}
	after, _ := strconv.ParseUint(r.Header.Get("Last-Event-ID"), 10, 64)

	ch := make(chan entry, 256)
	s.mu.Lock()
	if len(s.subs) >= streamMaxSubscribers {
		s.mu.Unlock()
		http.Error(w, "too many log stream subscribers", http.StatusServiceUnavailable)
		return
	}
	backlog := make([]entry, 0, len(s.ring))
	for i := range s.ring {
		e := s.ring[(s.next+i)%len(s.ring)]
		if e.seq > after && match(e) {
			backlog = append(backlog, e)
		}
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

	send := func(e entry) {
		fmt.Fprintf(w, "id: %d\ndata: %s\n\n", e.seq, e.line)
	}
	for _, e := range backlog {
		send(e)
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
		case e := <-ch:
			if e.seq > after && match(e) {
				send(e)
				flusher.Flush()
			}
		}
	}
}
