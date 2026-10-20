package llmhttp

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRingRecordsExchange(t *testing.T) {
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Request-Id", "up-1")
		w.Header().Set("Set-Cookie", "session=sekrit")
		w.Header().Set("Anthropic-Ratelimit-Tokens-Remaining", "1000")
		w.Write([]byte("event: a\ndata: {}\n\n"))
		w.(http.Flusher).Flush()
		w.Write([]byte("event: b\ndata: {}\n\n"))
	}))
	defer server.Close()

	ring := NewRing(10, 1<<20)
	client := &http.Client{Transport: &Transport{Log: ring}}
	ctx := WithProvider(WithModelID(WithConversationID(t.Context(), "conv-1"), "m-1"), "anthropic")
	req, _ := http.NewRequestWithContext(ctx, "POST", server.URL+"/v1/messages?key=sekrit&beta=true", strings.NewReader(`{"hello":"world"}`))
	req.Header.Set("X-Api-Key", "sekrit")
	req.Header.Set("Authorization", "Bearer sekrit")
	req.Header.Set("Anthropic-Version", "2023-06-01")
	req.Header.Set("X-Access-Token", "sekrit")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if gotBody != `{"hello":"world"}` {
		t.Fatalf("server got body %q", gotBody)
	}

	list := ring.List()
	if len(list) != 1 || list[0].Done || list[0].Status != 200 {
		t.Fatalf("before reading body: %+v", list)
	}

	respBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	d, ok := ring.Get(list[0].ID)
	if !ok {
		t.Fatal("exchange not found")
	}
	if !d.Done || d.Error != "" || d.DurationMs < d.TTFBMs {
		t.Errorf("exchange = %+v", d)
	}
	if d.Provider != "anthropic" || d.Model != "m-1" || d.ConversationID != "conv-1" || d.RequestID == "" {
		t.Errorf("context fields = %+v", d)
	}
	if d.RequestBody != `{"hello":"world"}` || d.ResponseBody != string(respBody) || d.ResponseBytes != len(respBody) {
		t.Errorf("bodies: req=%q resp=%q", d.RequestBody, d.ResponseBody)
	}
	if strings.Contains(d.URL, "sekrit") || !strings.Contains(d.URL, "beta=true") {
		t.Errorf("URL = %q", d.URL)
	}
	for _, h := range []string{"X-Api-Key", "Authorization", "X-Access-Token"} {
		if got := d.RequestHeader.Get(h); got != redacted {
			t.Errorf("%s = %q, want redacted", h, got)
		}
	}
	if d.RequestHeader.Get("Anthropic-Version") != "2023-06-01" || d.ResponseHeader.Get("X-Request-Id") != "up-1" {
		t.Errorf("headers: req=%v resp=%v", d.RequestHeader, d.ResponseHeader)
	}
	if d.ResponseHeader.Get("Anthropic-Ratelimit-Tokens-Remaining") != "1000" {
		t.Errorf("rate-limit header redacted: %v", d.ResponseHeader)
	}
	if d.ResponseHeader.Get("Set-Cookie") != redacted || resp.Header.Get("Set-Cookie") != "session=sekrit" {
		t.Errorf("Set-Cookie: recorded %q, caller saw %q", d.ResponseHeader.Get("Set-Cookie"), resp.Header.Get("Set-Cookie"))
	}
}

// TestRingLeavesUnreplayableBodyAlone checks that a request body that can't
// be replayed is sent intact and simply not captured.
func TestRingLeavesUnreplayableBodyAlone(t *testing.T) {
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
	}))
	defer server.Close()

	ring := NewRing(10, 1<<20)
	client := &http.Client{Transport: &Transport{Log: ring}}
	req, _ := http.NewRequestWithContext(t.Context(), "POST", server.URL, io.NopCloser(strings.NewReader("streamed")))
	if req.GetBody != nil {
		t.Fatal("test needs a request without GetBody")
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if gotBody != "streamed" {
		t.Fatalf("server got body %q", gotBody)
	}
	if list := ring.List(); len(list) != 1 || list[0].RequestBytes != 0 {
		t.Fatalf("list = %+v", list)
	}

	// A failing GetBody must not fail the request either.
	req, _ = http.NewRequestWithContext(t.Context(), "POST", server.URL, strings.NewReader("sent"))
	req.GetBody = func() (io.ReadCloser, error) { return nil, errors.New("no replay") }
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if gotBody != "sent" {
		t.Fatalf("server got body %q", gotBody)
	}
}

// TestRingBoundsInFlightResponses checks that streaming responses count
// against the byte budget as they arrive, and that an evicted exchange
// stops capturing.
func TestRingBoundsInFlightResponses(t *testing.T) {
	chunk := strings.Repeat("x", 60)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(chunk))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()

	ring := NewRing(10, 100)
	client := &http.Client{Transport: &Transport{Log: ring}}
	open := func() *http.Response {
		t.Helper()
		resp, err := client.Get(server.URL)
		if err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, len(chunk))
		if _, err := io.ReadFull(resp.Body, buf); err != nil {
			t.Fatal(err)
		}
		return resp
	}
	first := open()
	defer first.Body.Close()
	second := open()
	defer second.Body.Close()

	list := ring.List()
	if len(list) != 1 || list[0].ID != 2 || list[0].Done || list[0].ResponseBytes != len(chunk) {
		t.Fatalf("list = %+v", list)
	}
	if ring.bytes != len(chunk) {
		t.Fatalf("ring.bytes = %d, want %d", ring.bytes, len(chunk))
	}
	first.Body.Close()
	if ring.bytes != len(chunk) {
		t.Fatalf("after closing evicted body, ring.bytes = %d", ring.bytes)
	}
}

func TestRingRecordsTransportError(t *testing.T) {
	ring := NewRing(10, 1<<20)
	boom := errors.New("boom")
	client := &http.Client{Transport: &Transport{Log: ring, Base: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, boom
	})}}
	req, _ := http.NewRequestWithContext(t.Context(), "POST", "http://example.invalid/", strings.NewReader("{}"))
	if _, err := client.Do(req); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	list := ring.List()
	if len(list) != 1 || !list[0].Done || list[0].Error != "boom" || list[0].RequestBytes != 2 {
		t.Fatalf("list = %+v", list)
	}
}

func TestRingEviction(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(w, r.Body)
	}))
	defer server.Close()

	ring := NewRing(3, 100)
	client := &http.Client{Transport: &Transport{Log: ring}}
	send := func(body string) {
		t.Helper()
		resp, err := client.Post(server.URL, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	ids := func() []int64 {
		var out []int64
		for _, s := range ring.List() {
			out = append(out, s.ID)
		}
		return out
	}

	for range 4 {
		send("x")
	}
	if got := ids(); len(got) != 3 || got[0] != 4 || got[2] != 2 {
		t.Fatalf("after count eviction ids = %v", got)
	}

	// Each exchange is 80 bytes (40 each way); two exceed the 100-byte budget.
	send(strings.Repeat("y", 40))
	send(strings.Repeat("y", 40))
	if got := ids(); len(got) != 1 || got[0] != 6 {
		t.Fatalf("after byte eviction ids = %v", got)
	}

	// The newest exchange is kept even when it alone exceeds the budget.
	send(strings.Repeat("z", 200))
	if got := ids(); len(got) != 1 || got[0] != 7 {
		t.Fatalf("after oversized ids = %v", got)
	}
	if _, ok := ring.Get(6); ok {
		t.Fatal("evicted exchange still retrievable")
	}
}

// TestRingRecordsIdleTimeout checks that the idle-stall error still reaches
// the caller through the capture wrapper and is recorded.
func TestRingRecordsIdleTimeout(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flushWrite(t, w, "data: hello\n\n")
		<-release
	}))
	defer server.Close()
	defer close(release)

	ring := NewRing(10, 1<<20)
	client := &http.Client{Transport: &Transport{Log: ring, IdleTimeout: 50 * time.Millisecond}}
	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, err = io.ReadAll(resp.Body)
	requireIdleStall(t, err)
	if list := ring.List(); len(list) != 1 || !list[0].Done || list[0].Error != err.Error() || list[0].ResponseBytes == 0 {
		t.Fatalf("list = %+v", list)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestRingRecordsResponseTimeline checks that the arrival of each streamed
// chunk is recorded against its end offset in the captured body.
func TestRingRecordsResponseTimeline(t *testing.T) {
	next := make(chan bool)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("data: 1\n\n"))
		w.(http.Flusher).Flush()
		<-next
		w.Write([]byte("data: 22\n\n"))
	}))
	defer server.Close()

	ring := NewRing(10, 1<<20)
	client := &http.Client{Transport: &Transport{Log: ring}}
	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 9)
	if _, err := io.ReadFull(resp.Body, buf); err != nil {
		t.Fatal(err)
	}
	id := ring.List()[0].ID
	if d, _ := ring.Get(id); len(d.ResponseTimeline) != 1 || d.ResponseTimeline[0][0] != 9 {
		t.Fatalf("after first chunk, timeline = %v", d.ResponseTimeline)
	}
	close(next)
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatal(err)
	}
	d, _ := ring.Get(id)
	tl := d.ResponseTimeline
	if len(tl) == 0 || tl[len(tl)-1][0] != int64(d.ResponseBytes) || d.ResponseBytes != 19 {
		t.Fatalf("timeline = %v, response bytes = %d", tl, d.ResponseBytes)
	}
	for i := 1; i < len(tl); i++ {
		if tl[i][0] <= tl[i-1][0] || tl[i][1] <= tl[i-1][1] {
			t.Errorf("timeline not increasing: %v", tl)
		}
	}
	if l := ring.List(); l[0].ResponseTimeline != nil {
		t.Errorf("List includes timeline: %v", l[0].ResponseTimeline)
	}
}
