package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/server/resource"
)

func initTestDiskSpace(t *testing.T, s *Server, available *uint64) *atomic.Int32 {
	t.Helper()
	calls := new(atomic.Int32)
	if err := s.initResourceMonitor(t.Context(), resource.Disk, func() (resourceReading, error) {
		calls.Add(1)
		return resourceReading{available: *available, total: 100_000_000_000}, nil
	}); err != nil {
		t.Fatal(err)
	}
	return calls
}

func checkDiskSpace(t *testing.T, m *resourceMonitor) resource.Status {
	t.Helper()
	if err := m.check(t.Context()); err != nil {
		t.Fatal(err)
	}
	return m.snapshot()
}

func TestDiskSpaceBoundaryAndEpisodes(t *testing.T) {
	t.Parallel()
	s, database, _ := newTestServer(t)
	var writes atomic.Int32
	database.Pool().OnCommit(func() { writes.Add(1) })
	available := resource.DiskThreshold
	calls := initTestDiskSpace(t, s, &available)
	m := s.diskSpace.Load()
	if got := m.snapshot(); got.Active || got.AvailableBytes != available || got.EpisodeID != 0 || calls.Load() != 1 || writes.Load() != 0 {
		t.Fatalf("exact boundary/startup: %+v, probes=%d, writes=%d", got, calls.Load(), writes.Load())
	}

	available--
	low := checkDiskSpace(t, m)
	if !low.Active || low.Dismissed || low.EpisodeID != 1 || low.Revision != 1 || writes.Load() != 1 {
		t.Fatalf("below boundary: %+v, writes=%d", low, writes.Load())
	}
	for range 3 {
		available--
		got := checkDiskSpace(t, m)
		if got.AvailableBytes != available || got.EpisodeID != low.EpisodeID || got.Revision != low.Revision || writes.Load() != 1 {
			t.Fatalf("sustained low should only update cache: %+v, writes=%d", got, writes.Load())
		}
	}
	value, err := database.GetSetting(t.Context(), diskSpaceSettingKey)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(value, "available_bytes") {
		t.Fatalf("persisted measurement: %s", value)
	}

	dismissed, err := m.dismiss(t.Context(), low.EpisodeID)
	if err != nil || !dismissed.Dismissed || dismissed.Revision != 2 || writes.Load() != 2 {
		t.Fatalf("dismiss: %+v, err=%v, writes=%d", dismissed, err, writes.Load())
	}
	again, err := m.dismiss(t.Context(), low.EpisodeID)
	if err != nil || again != dismissed || writes.Load() != 2 {
		t.Fatalf("idempotent dismissal: %+v, err=%v, writes=%d", again, err, writes.Load())
	}

	available = resource.DiskThreshold
	recovered := checkDiskSpace(t, m)
	if recovered.Active || recovered.Dismissed || recovered.EpisodeID != low.EpisodeID || recovered.Revision != 3 || writes.Load() != 3 {
		t.Fatalf("recovery at exact boundary: %+v, writes=%d", recovered, writes.Load())
	}
	available++
	checkDiskSpace(t, m)
	if writes.Load() != 3 {
		t.Fatal("sustained healthy wrote settings")
	}

	available = 0
	later := checkDiskSpace(t, m)
	if !later.Active || later.Dismissed || later.EpisodeID <= low.EpisodeID || later.Revision != 4 || writes.Load() != 4 {
		t.Fatalf("later episode did not rearm: %+v, writes=%d", later, writes.Load())
	}
	stale, err := m.dismiss(t.Context(), low.EpisodeID)
	if err != nil || stale != later || writes.Load() != 4 {
		t.Fatalf("stale dismissal changed newer episode: %+v, err=%v", stale, err)
	}
}

func TestDiskSpaceDismissalPersistence(t *testing.T) {
	t.Parallel()
	s, database, _ := newTestServer(t)
	available := resource.DiskThreshold - 1
	initTestDiskSpace(t, s, &available)
	dismissed, err := s.diskSpace.Load().dismiss(t.Context(), s.diskSpace.Load().snapshot().EpisodeID)
	if err != nil {
		t.Fatal(err)
	}
	path := database.Path()
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := db.New(db.Config{DSN: path})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restarted := NewServer(reopened, s.llmManager, s.toolSetConfig, s.logger, true, "predictable", "")
	var writes atomic.Int32
	reopened.Pool().OnCommit(func() { writes.Add(1) })
	initTestDiskSpace(t, restarted, &available)
	if got := restarted.diskSpace.Load().snapshot(); got != dismissed || writes.Load() != 0 {
		t.Fatalf("restart lost dismissal: %+v, want %+v; writes=%d", got, dismissed, writes.Load())
	}
	// A startup observation of recovery also rearms the next episode.
	available = resource.DiskThreshold
	initTestDiskSpace(t, restarted, &available)
	if got := restarted.diskSpace.Load().snapshot(); got.Active || got.Dismissed || got.EpisodeID != dismissed.EpisodeID {
		t.Fatalf("startup recovery: %+v", got)
	}
	available--
	initTestDiskSpace(t, restarted, &available)
	if got := restarted.diskSpace.Load().snapshot(); !got.Active || got.Dismissed || got.EpisodeID <= dismissed.EpisodeID {
		t.Fatalf("next startup episode: %+v", got)
	}
}

func TestDiskSpaceProbeErrors(t *testing.T) {
	t.Parallel()
	s, _, _ := newTestServer(t)
	probeErr := errors.New("probe failed")
	if err := s.initResourceMonitor(t.Context(), resource.Disk, func() (resourceReading, error) { return resourceReading{}, probeErr }); !errors.Is(err, probeErr) || s.diskSpace.Load() != nil {
		t.Fatalf("startup must fail, not report healthy: monitor=%v, err=%v", s.diskSpace.Load(), err)
	}
	available := resource.DiskThreshold - 1
	initTestDiskSpace(t, s, &available)
	before := s.diskSpace.Load().snapshot()
	s.diskSpace.Load().probe = func() (resourceReading, error) { return resourceReading{}, probeErr }
	if err := s.diskSpace.Load().check(t.Context()); !errors.Is(err, probeErr) || s.diskSpace.Load().snapshot() != before {
		t.Fatalf("probe error changed status: %+v, err=%v", s.diskSpace.Load().snapshot(), err)
	}
	if _, err := readDisk(filepath.Join(t.TempDir(), "missing.db")); err == nil {
		t.Fatal("statfs on missing path succeeded")
	}
	if r, err := readDisk(s.db.Path()); err != nil || r.total == 0 || r.available > r.total {
		t.Fatalf("statfs on actual SQLite path: %+v, %v", r, err)
	}
}

func dismissResourceHTTP(t *testing.T, handler http.Handler, kind resource.Kind, id uint64) resource.Status {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/resources/"+string(kind)+"/dismiss", strings.NewReader(fmt.Sprintf(`{"episode_id":%d}`, id)))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("dismiss: HTTP %d: %s", w.Code, w.Body.String())
	}
	var status resource.Status
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	return status
}

func TestDiskSpaceDismissRoute(t *testing.T) {
	t.Parallel()
	s, database, _ := newTestServer(t)
	available := resource.DiskThreshold - 1
	initTestDiskSpace(t, s, &available)
	mux := http.NewServeMux()
	s.RegisterRoutes(mux)
	initial := s.diskSpace.Load().snapshot()
	if stale := dismissResourceHTTP(t, mux, resource.Disk, initial.EpisodeID+1); stale != initial {
		t.Fatalf("unknown ID changed status: %+v", stale)
	}
	dismissed := dismissResourceHTTP(t, mux, resource.Disk, initial.EpisodeID)
	if !dismissed.Dismissed || dismissed != s.diskSpace.Load().snapshot() {
		t.Fatalf("response is not authoritative: %+v", dismissed)
	}
	if got := dismissResourceHTTP(t, mux, resource.Disk, initial.EpisodeID); got != dismissed {
		t.Fatalf("repeat dismissal: %+v", got)
	}
	available = resource.DiskThreshold
	checkDiskSpace(t, s.diskSpace.Load())
	available--
	later := checkDiskSpace(t, s.diskSpace.Load())
	if got := dismissResourceHTTP(t, mux, resource.Disk, initial.EpisodeID); got != later {
		t.Fatalf("stale route dismissal changed later episode: %+v", got)
	}
	for _, body := range []string{`{`, `{}`, `{"episode_id":0}`, `{"episode_id":-1}`, `{"episode_id":"1"}`} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/resources/disk/dismiss", strings.NewReader(body)))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("invalid body %s: HTTP %d", body, w.Code)
		}
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/resources/disk/dismiss", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET dismissal: HTTP %d", w.Code)
	}
	// Unknown kinds, and kinds without a monitor (memory off Linux), are 404s.
	for _, kind := range []string{"cpu", "memory"} {
		w = httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/resources/"+kind+"/dismiss", strings.NewReader(`{"episode_id":1}`)))
		if w.Code != http.StatusNotFound {
			t.Fatalf("dismiss %s: HTTP %d", kind, w.Code)
		}
	}

	before, err := database.GetSetting(t.Context(), diskSpaceSettingKey)
	if err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/settings", strings.NewReader(fmt.Sprintf(`{"key":%q,"value":"{}"}`, diskSpaceSettingKey))))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("internal key writable via /settings: HTTP %d", w.Code)
	}
	after, err := database.GetSetting(t.Context(), diskSpaceSettingKey)
	if err != nil || after != before {
		t.Fatalf("internal setting was changed: %q, err=%v", after, err)
	}
}

func TestDiskSpaceDismissWriteError(t *testing.T) {
	t.Parallel()
	s, database, _ := newTestServer(t)
	available := resource.DiskThreshold - 1
	initTestDiskSpace(t, s, &available)
	before := s.diskSpace.Load().snapshot()
	// Fail at COMMIT, not in the settings statement itself.
	if err := database.Pool().Exec(t.Context(), `
		CREATE TABLE disk_parent (id INTEGER PRIMARY KEY);
		CREATE TABLE disk_child (id INTEGER REFERENCES disk_parent(id) DEFERRABLE INITIALLY DEFERRED);
		CREATE TRIGGER fail_disk_dismiss AFTER UPDATE ON settings WHEN NEW.key = 'internal.disk_space'
		BEGIN INSERT INTO disk_child VALUES (1); END;
	`); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	s.RegisterRoutes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/resources/disk/dismiss", strings.NewReader(`{"episode_id":1}`)))
	if w.Code != http.StatusInternalServerError || s.diskSpace.Load().snapshot() != before {
		t.Fatalf("failed commit reported a saved dismissal: HTTP %d, status=%+v", w.Code, s.diskSpace.Load().snapshot())
	}
	initTestDiskSpace(t, s, &available)
	if s.diskSpace.Load().snapshot() != before {
		t.Fatal("failed commit persisted dismissal")
	}
}

// Read complete SSE frames rather than polling or sleeping. Each HTTP request
// has a deadline so a missing snapshot/update fails instead of hanging.
func openResourceStream(t *testing.T, url string) (*bufio.Scanner, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		cancel()
		t.Fatalf("stream HTTP %d", resp.StatusCode)
	}
	closeStream := func() { cancel(); resp.Body.Close() }
	t.Cleanup(closeStream)
	return bufio.NewScanner(resp.Body), closeStream
}

func readDiskSpaceFrame(t *testing.T, scanner *bufio.Scanner) resource.Status {
	t.Helper()
	status := readResourceFrame(t, scanner)
	if status.Kind != resource.Disk {
		t.Fatalf("not a disk frame: %+v", status)
	}
	return status
}

func readResourceFrame(t *testing.T, scanner *bufio.Scanner) resource.Status {
	t.Helper()
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var frame StreamResponse
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &frame); err != nil {
			t.Fatal(err)
		}
		if frame.ResourceStatus != nil {
			if frame.ConversationID != "" {
				t.Fatalf("resource event is not global: %+v", frame)
			}
			return *frame.ResourceStatus
		}
	}
	t.Fatalf("missing resource frame: %v", scanner.Err())
	return resource.Status{}
}

func TestDiskSpaceStreamSnapshotAndGlobalDelivery(t *testing.T) {
	t.Parallel()
	s, _, _ := newTestServer(t)
	available := resource.DiskThreshold
	calls := initTestDiskSpace(t, s, &available)
	mux := http.NewServeMux()
	s.RegisterRoutes(mux)
	httpServer := httptest.NewServer(mux)
	defer httpServer.Close()

	// Each new stream samples once (probe count +1) so an idle Shelley shows
	// the current state on page load, and the snapshot reflects that sample.
	available++
	a, closeA := openResourceStream(t, httpServer.URL+"/api/stream2")
	defer closeA()
	initial := readDiskSpaceFrame(t, a)
	if initial != s.diskSpace.Load().snapshot() || initial.AvailableBytes != available || calls.Load() != 2 {
		t.Fatalf("initial snapshot: %+v, probes=%d", initial, calls.Load())
	}
	// Also cover caught-up reconnects, where no initial list patch is sent.
	s.conversationListStream.mu.Lock()
	hash := s.conversationListStream.currentHash
	s.conversationListStream.mu.Unlock()
	available--
	b, closeB := openResourceStream(t, httpServer.URL+"/api/stream2?conversation_list_hash="+hash)
	defer closeB()
	if got := readDiskSpaceFrame(t, b); got.AvailableBytes != available || calls.Load() != 3 {
		t.Fatalf("reconnect snapshot: %+v, probes=%d", got, calls.Load())
	}
	// A healthy sample on connect is not broadcast to existing subscribers.
	assertBoth := func(want resource.Status) {
		t.Helper()
		for _, scanner := range []*bufio.Scanner{a, b} {
			if got := readDiskSpaceFrame(t, scanner); got != want {
				t.Fatalf("global transition: %+v, want %+v", got, want)
			}
		}
	}
	available--
	low := checkDiskSpace(t, s.diskSpace.Load())
	assertBoth(low)
	// Sustained low while visible pushes the fresh number on the same revision.
	available--
	refreshed := checkDiskSpace(t, s.diskSpace.Load())
	if refreshed.Revision != low.Revision || refreshed.AvailableBytes != available {
		t.Fatalf("refresh: %+v", refreshed)
	}
	assertBoth(refreshed)
	dismissed := dismissResourceHTTP(t, mux, resource.Disk, low.EpisodeID)
	assertBoth(dismissed)
	closeB()
	b, closeB = openResourceStream(t, httpServer.URL+"/api/stream2?conversation_list_hash="+hash)
	defer closeB()
	// Dismissed: the connect sample refreshes bytes silently; the snapshot
	// carries the dismissed episode and no frame reaches the other stream.
	if got := readDiskSpaceFrame(t, b); !got.Dismissed || got.EpisodeID != dismissed.EpisodeID || got.Revision != dismissed.Revision || calls.Load() != 6 {
		t.Fatalf("dismissed reconnect: %+v, probes=%d", got, calls.Load())
	}
	available = resource.DiskThreshold
	assertBoth(checkDiskSpace(t, s.diskSpace.Load()))
	available--
	assertBoth(checkDiskSpace(t, s.diskSpace.Load()))
}

type diskSpaceLogWriter struct{ lines chan string }

func (w diskSpaceLogWriter) Write(p []byte) (int, error) {
	w.lines <- string(p)
	return len(p), nil
}

// refreshResources samples every monitor once, logs probe errors without
// clearing a live warning, and is a no-op before init.
func TestResourceRefresh(t *testing.T) {
	t.Parallel()
	s, _, _ := newTestServer(t)
	s.refreshResources(t.Context()) // uninitialized: must not panic
	available := resource.DiskThreshold
	calls := initTestDiskSpace(t, s, &available)
	available--
	s.refreshResources(t.Context())
	if got := s.diskSpace.Load().snapshot(); !got.Active || calls.Load() != 2 {
		t.Fatalf("refresh did not sample: %+v, probes=%d", got, calls.Load())
	}
	before := s.diskSpace.Load().snapshot()
	s.diskSpace.Load().probe = func() (resourceReading, error) { return resourceReading{}, errors.New("refresh probe failed") }
	lines := make(chan string, 1)
	s.logger = slog.New(slog.NewTextHandler(diskSpaceLogWriter{lines}, nil))
	s.refreshResources(t.Context())
	if line := <-lines; !strings.Contains(line, "refresh probe failed") || !strings.Contains(line, "kind=disk") {
		t.Fatalf("missing error log: %s", line)
	}
	if s.diskSpace.Load().snapshot() != before {
		t.Fatal("probe error cleared warning")
	}

	// Dismissed: sustained-low samples are silent; the next event is recovery.
	next, _ := s.streamPub.SubscribeWithStatus(t.Context(), -1)
	s.diskSpace.Load().probe = func() (resourceReading, error) {
		return resourceReading{available: available, total: 100_000_000_000}, nil
	}
	if _, err := s.diskSpace.Load().dismiss(t.Context(), before.EpisodeID); err != nil {
		t.Fatal(err)
	}
	if ev, ok := next(); !ok || ev.ResourceStatus == nil || !ev.ResourceStatus.Dismissed {
		t.Fatalf("expected dismissal event, got %+v", ev)
	}
	available--
	s.refreshResources(t.Context())
	available = resource.DiskThreshold
	s.refreshResources(t.Context())
	if ev, ok := next(); !ok || ev.ResourceStatus == nil || ev.ResourceStatus.Active {
		t.Fatalf("expected recovery event with no refresh leak before it, got %+v", ev)
	}
}

// Every message an agent loop records samples the resources, so pressure from
// a tool shows up while the turn is still running rather than at its end.
func TestRecordedMessagesSampleResources(t *testing.T) {
	t.Parallel()
	s, database, _ := newTestServer(t)
	conversation, err := database.CreateConversation(t.Context(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := s.getOrCreateConversationManager(t.Context(), conversation.ConversationID, "")
	if err != nil {
		t.Fatal(err)
	}
	// Each probe after the startup one reports whether the turn was running.
	working := make(chan bool, 10)
	started := false
	if err := s.initResourceMonitor(t.Context(), resource.Memory, func() (resourceReading, error) {
		if started {
			working <- manager.IsAgentWorking()
		}
		started = true
		return resourceReading{available: 1, total: 100}, nil
	}); err != nil {
		t.Fatal(err)
	}
	postChatMessage(t, s, conversation.ConversationID, "bash: echo hi")
	// The tool call and its result mid-turn, then the closing reply.
	for i, want := range []bool{true, true, false} {
		select {
		case got := <-working:
			if got != want {
				t.Fatalf("probe %d: agent working = %v, want %v", i, got, want)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("probe %d never ran", i)
		}
	}
	waitForIdle(t, s, conversation.ConversationID)
	if len(working) != 0 {
		t.Fatalf("%d extra probes", len(working))
	}
}

func TestDiskSpaceCriticalEscalation(t *testing.T) {
	t.Parallel()
	s, database, _ := newTestServer(t)
	available := resource.DiskThreshold - 1
	initTestDiskSpace(t, s, &available)
	m := s.diskSpace.Load()
	low := m.snapshot()
	if low.Critical {
		t.Fatalf("low is not critical: %+v", low)
	}
	if _, err := m.dismiss(t.Context(), low.EpisodeID); err != nil {
		t.Fatal(err)
	}

	// Exactly the critical boundary is still just low: dismissed stays.
	available = resource.DiskCriticalThreshold
	if got := checkDiskSpace(t, m); got.Critical || !got.Dismissed || got.Revision != 2 {
		t.Fatalf("at critical boundary: %+v", got)
	}
	// Below it: escalate once. Same episode, un-dismissed, persisted.
	available--
	crit := checkDiskSpace(t, m)
	if !crit.Critical || crit.Dismissed || crit.EpisodeID != low.EpisodeID || crit.Revision != 3 {
		t.Fatalf("critical escalation: %+v", crit)
	}
	value, err := database.GetSetting(t.Context(), diskSpaceSettingKey)
	if err != nil || !strings.Contains(value, `"critical":true`) {
		t.Fatalf("critical not persisted: %s, err=%v", value, err)
	}
	// Dismiss critical, then climb back above 500 MB: latched, no re-show.
	if _, err := m.dismiss(t.Context(), crit.EpisodeID); err != nil {
		t.Fatal(err)
	}
	available = resource.DiskThreshold - 1
	if got := checkDiskSpace(t, m); !got.Critical || !got.Dismissed || got.Revision != 4 {
		t.Fatalf("critical must latch within the episode: %+v", got)
	}
	// Dropping again does not escalate a second time.
	available = 0
	if got := checkDiskSpace(t, m); !got.Dismissed || got.Revision != 4 {
		t.Fatalf("critical escalated twice: %+v", got)
	}
	// Recovery clears everything; a fresh low episode starts non-critical.
	available = resource.DiskThreshold
	if got := checkDiskSpace(t, m); got.Active || got.Critical || got.Dismissed || got.Revision != 5 {
		t.Fatalf("recovery: %+v", got)
	}
	available = resource.DiskThreshold - 1
	if got := checkDiskSpace(t, m); !got.Active || got.Critical || got.EpisodeID != low.EpisodeID+1 {
		t.Fatalf("new episode: %+v", got)
	}
	// Straight from healthy to critical: one transition, critical from the start.
	available = resource.DiskThreshold
	checkDiskSpace(t, m)
	available = 0
	if got := checkDiskSpace(t, m); !got.Active || !got.Critical || got.Dismissed || got.EpisodeID != low.EpisodeID+2 {
		t.Fatalf("direct critical: %+v", got)
	}

	// Persistence: critical survives a restart with the dismissal intact.
	if _, err := m.dismiss(t.Context(), low.EpisodeID+2); err != nil {
		t.Fatal(err)
	}
	restarted := NewServer(database, s.llmManager, s.toolSetConfig, s.logger, true, "predictable", "")
	initTestDiskSpace(t, restarted, &available)
	if got := restarted.diskSpace.Load().snapshot(); !got.Critical || !got.Dismissed || got.EpisodeID != low.EpisodeID+2 {
		t.Fatalf("after restart: %+v", got)
	}
}

// fillTestDB stops the database file from growing, then uses up its free pages
// and the slack in the settings table, so inserts there (and anything that
// needs a new page) fail with a real SQLITE_FULL.
func fillTestDB(t *testing.T, database *db.DB) {
	t.Helper()
	ctx := t.Context()
	p := database.Pool()
	if err := p.Exec(ctx, "CREATE TABLE disk_full_filler (b BLOB)"); err != nil {
		t.Fatal(err)
	}
	var pages int
	if err := p.Rx(ctx, func(ctx context.Context, rx *db.Rx) error {
		return rx.Conn().QueryRowContext(ctx, "PRAGMA page_count").Scan(&pages)
	}); err != nil {
		t.Fatal(err)
	}
	if err := p.Exec(ctx, fmt.Sprintf("PRAGMA max_page_count=%d", pages)); err != nil {
		t.Fatal(err)
	}
	for _, insert := range []func(i int) error{
		func(int) error { return p.Exec(ctx, "INSERT INTO disk_full_filler VALUES (zeroblob(100))") },
		func(i int) error { return database.SetSetting(ctx, fmt.Sprintf("test.filler.%d", i), "x") },
	} {
		for i := 0; ; i++ {
			err := insert(i)
			if db.IsDiskFull(err) {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			if i > 100_000 {
				t.Fatal("database never filled up")
			}
		}
	}
}

// When the disk fills up, a failed write tells the client so and raises the
// notice right away, even though the notice itself cannot be persisted yet.
func TestDiskFullWriteReportsAndRaisesNotice(t *testing.T) {
	t.Parallel()
	s, database, _ := newTestServer(t)
	fillTestDB(t, database) // before init, so filling raises nothing
	available := resource.DiskThreshold
	initTestDiskSpace(t, s, &available)
	m := s.diskSpace.Load()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	next, _ := s.streamPub.SubscribeWithStatus(ctx, -1)

	available = 0
	body := `{"message":"hi","model":"predictable"}`
	req := httptest.NewRequest(http.MethodPost, "/api/conversations/new", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handleNewConversation(w, req)
	if w.Code != http.StatusInsufficientStorage || !strings.Contains(w.Body.String(), "database or disk is full") {
		t.Fatalf("got HTTP %d %q, want 507 with the sqlite error", w.Code, w.Body.String())
	}

	var raised *resource.Status
	for raised == nil {
		ev, ok := next()
		if !ok {
			t.Fatal("disk-full write did not raise the notice")
		}
		raised = ev.ResourceStatus
	}
	if !raised.Active || !raised.Critical || raised.Dismissed || raised.AvailableBytes != 0 {
		t.Fatalf("raised notice: %+v", raised)
	}
	m.mu.Lock() // the background check has finished (and failed to persist)
	m.mu.Unlock()
	if value, err := database.GetSetting(t.Context(), diskSpaceSettingKey); err != nil || value != "" {
		t.Fatalf("notice persisted on a full disk? %q, err=%v", value, err)
	}

	// Once there is room again, the next check catches the persisted episode up.
	if err := database.Pool().Exec(t.Context(), "PRAGMA max_page_count=1073741823"); err != nil {
		t.Fatal(err)
	}
	status := checkDiskSpace(t, m)
	value, err := database.GetSetting(t.Context(), diskSpaceSettingKey)
	if err != nil {
		t.Fatal(err)
	}
	var saved resourceEpisode
	if err := json.Unmarshal([]byte(value), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Revision != status.Revision || !saved.Active || !saved.Critical {
		t.Fatalf("saved %+v, want %+v", saved, status)
	}
}

// Starting on a full disk must not fail: the notice is shown and saved later.
func TestDiskSpaceInitOnFullDisk(t *testing.T) {
	t.Parallel()
	s, database, _ := newTestServer(t)
	fillTestDB(t, database)
	available := uint64(0)
	initTestDiskSpace(t, s, &available)
	if status := s.diskSpace.Load().snapshot(); !status.Active || !status.Critical {
		t.Fatalf("status = %+v, want active and critical", status)
	}
	if value, err := database.GetSetting(t.Context(), diskSpaceSettingKey); err != nil || value != "" {
		t.Fatalf("notice persisted on a full disk? %q, err=%v", value, err)
	}
}

// A full disk at startup must not stop Shelley serving, or the user only sees
// the proxy's bare 502. Clearing agent_working left by a turn that was running
// when the disk filled is a write the full disk can fail.
func TestStartOnFullDiskWithWorkingConversation(t *testing.T) {
	t.Parallel()
	s, database, _ := newTestServer(t)
	seedInterruptedConversation(t, database, nil)
	// In-place updates need no new pages, so make them grow the database.
	if err := database.Pool().Exec(t.Context(), `CREATE TRIGGER grow_on_update AFTER UPDATE ON conversations BEGIN
		INSERT OR REPLACE INTO settings (key, value) VALUES ('test.grow', hex(zeroblob(4096)));
	END`); err != nil {
		t.Fatal(err)
	}
	fillTestDB(t, database)
	if _, err := database.ConsumeResumeAfterUpgrade(t.Context()); !db.IsDiskFull(err) {
		t.Fatalf("ConsumeResumeAfterUpgrade error = %v, want disk full", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	started := make(chan error, 1)
	go func() { started <- s.StartWithListeners(listener, "") }()
	served := make(chan error, 1)
	go func() {
		resp, err := http.Get("http://" + listener.Addr().String() + "/api/conversations")
		if err == nil {
			resp.Body.Close()
		}
		served <- err
	}()
	select {
	case err := <-started:
		t.Fatalf("server exited: %v", err)
	case err := <-served:
		if err != nil {
			t.Fatal(err)
		}
	}
}

// The UI takes a connection's resource statuses as one snapshot, completed by
// the first frame that is neither a list patch nor a status: pin that order.
func TestStreamOpensWithListReplayThenResourceSnapshot(t *testing.T) {
	t.Parallel()
	s, database, _ := newTestServer(t)
	if _, err := database.CreateConversation(t.Context(), strPtr("replayed"), true, nil, nil, db.ConversationOptions{}); err != nil {
		t.Fatal(err)
	}
	available := resource.DiskThreshold - 1
	initTestDiskSpace(t, s, &available)
	memory := func() (resourceReading, error) { return resourceReading{available: 500, total: 1000}, nil }
	if err := s.initResourceMonitor(t.Context(), resource.Memory, memory); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	s.RegisterRoutes(mux)
	httpServer := httptest.NewServer(mux)
	defer httpServer.Close()

	stream, closeStream := openResourceStream(t, httpServer.URL+"/api/stream2")
	defer closeStream()
	var order []string
	for stream.Scan() {
		line, ok := strings.CutPrefix(stream.Text(), "data: ")
		if !ok {
			continue
		}
		var frame StreamResponse
		if err := json.Unmarshal([]byte(line), &frame); err != nil {
			t.Fatal(err)
		}
		switch {
		case frame.ConversationListPatch != nil:
			order = append(order, "list")
		case frame.ResourceStatus != nil:
			order = append(order, string(frame.ResourceStatus.Kind))
		default:
			order = append(order, "other")
		}
		if order[len(order)-1] == "other" {
			break
		}
	}
	if got := strings.Join(order, ","); got != "list,disk,memory,other" {
		t.Fatalf("opening frames: %s", got)
	}
}
