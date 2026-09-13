package obs

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func logLine(event string) []byte {
	return fmt.Appendf(nil, `{"msg":%q,"event":%q}`+"\n", event, event)
}

// readEvents collects SSE data lines until n arrive or the timeout passes.
func readEvents(t *testing.T, url string, n int) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var got []string
	sc := bufio.NewScanner(resp.Body)
	for len(got) < n && sc.Scan() {
		if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
			got = append(got, data)
		}
	}
	return got
}

// A subscriber gets the buffered lines in order, then lines written after it connected.
func TestStreamBacklogThenLive(t *testing.T) {
	s := NewStream()
	srv := httptest.NewServer(s)
	defer srv.Close()

	_, _ = s.Write(logLine("transfer.created"))
	_, _ = s.Write(logLine("transfer.declined"))
	go func() {
		time.Sleep(100 * time.Millisecond)
		_, _ = s.Write(logLine("transfer.credited"))
	}()

	got := readEvents(t, srv.URL, 3)
	if len(got) != 3 ||
		!strings.Contains(got[0], "transfer.created") ||
		!strings.Contains(got[1], "transfer.declined") ||
		!strings.Contains(got[2], "transfer.credited") {
		t.Fatalf("got %v", got)
	}
}

// Old lines fall out of the ring, and writes never block on a subscriber that isn't reading.
func TestStreamRingAndSlowSubscriber(t *testing.T) {
	s := NewStream()
	stuck := make(chan []byte) // unbuffered and never read
	s.subs[stuck] = struct{}{}

	done := make(chan struct{})
	go func() {
		for i := range streamBuffer + 10 {
			_, _ = s.Write(logLine(fmt.Sprintf("event.%d", i)))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Write blocked on a slow subscriber")
	}
	if len(s.ring) != streamBuffer {
		t.Fatalf("ring holds %d lines, want %d", len(s.ring), streamBuffer)
	}
	if oldest := string(s.ring[s.next]); !strings.Contains(oldest, `"event.10"`) {
		t.Fatalf("oldest retained line %s, want event.10", oldest)
	}
}
