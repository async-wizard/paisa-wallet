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

func logLine(event, requestID string) []byte {
	return []byte(fmt.Sprintf(`{"message":%q,"event":%q,"request_id":%q}`+"\n", event, event, requestID))
}

// readEvents collects SSE data lines until n arrive or the timeout passes.
func readEvents(t *testing.T, url string, header http.Header, n int) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	for k, v := range header {
		req.Header[k] = v
	}
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

func TestStreamFiltersBacklogAndResumes(t *testing.T) {
	s := NewStream()
	srv := httptest.NewServer(s)
	defer srv.Close()

	for _, l := range [][]byte{
		logLine("transfer.created", "r1"),
		logLine("transfer.declined", "r1"),
		logLine("transfer.created", "r2"),
		logLine("http.request", "r1"),
	} {
		_, _ = s.Write(l)
	}

	if got := readEvents(t, srv.URL+"?event=transfer.created", nil, 2); len(got) != 2 {
		t.Fatalf("event filter: got %d lines, want 2: %v", len(got), got)
	}
	got := readEvents(t, srv.URL+"?correlation_id=r1", nil, 3)
	if len(got) != 3 || !strings.Contains(got[1], "transfer.declined") {
		t.Fatalf("correlation filter: got %v", got)
	}

	// Resuming after id 2 skips the first two lines. The stream stays open, so a line
	// written afterwards must also arrive.
	go func() {
		time.Sleep(100 * time.Millisecond)
		_, _ = s.Write(logLine("transfer.credited", "r3"))
	}()
	got = readEvents(t, srv.URL, http.Header{"Last-Event-ID": {"2"}}, 3)
	if len(got) != 3 || !strings.Contains(got[0], `"r2"`) || !strings.Contains(got[2], "transfer.credited") {
		t.Fatalf("resume: got %v", got)
	}
}

// Old lines fall out of the ring, and writes never block on a subscriber that isn't reading.
func TestStreamRingAndSlowSubscriber(t *testing.T) {
	s := NewStream()
	stuck := make(chan entry) // unbuffered and never read
	s.subs[stuck] = struct{}{}

	done := make(chan struct{})
	go func() {
		for i := range streamBuffer + 10 {
			_, _ = s.Write(logLine("http.request", fmt.Sprint(i)))
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
	oldest := s.ring[s.next]
	if oldest.seq != 11 {
		t.Fatalf("oldest retained seq %d, want 11", oldest.seq)
	}
}
