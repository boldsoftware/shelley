package test

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"sync"
	"testing"
)

// Ported from ui/e2e/resource-notice.spec.ts.

// resourceStatus is a resource status as the server streams it.
type resourceStatus map[string]any

// with is a copy of r with changes.
func (r resourceStatus) with(changes resourceStatus) resourceStatus {
	c := maps.Clone(r)
	maps.Copy(c, changes)
	return c
}

var (
	lowDisk = resourceStatus{
		"kind":            "disk",
		"episode_id":      7,
		"revision":        3,
		"active":          true,
		"critical":        false,
		"dismissed":       false,
		"available_bytes": 1_600_000_000,
		"total_bytes":     79_000_000_000,
		"stall_percent":   0,
	}
	highMemory = lowDisk.with(resourceStatus{
		"kind":            "memory",
		"episode_id":      2,
		"revision":        5,
		"available_bytes": 1_200_000_000,
		"total_bytes":     16_762_000_000,
		"stall_percent":   41.7,
	})
)

// resourceStream answers every /api/stream2 connection like the server: one
// frame per status statuses gives, then a heartbeat, which completes the
// resource snapshot. The body ends right away, so the client reconnects
// about a second later, with a query string once it knows the conversation
// list's hash; both hit this.
func (s *shelley) resourceStream(statuses func() []resourceStatus) {
	s.route("/api/stream2", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, status := range statuses() {
			fmt.Fprintf(w, "data: %s\n\n", mustJSON(map[string]any{"resource_status": status}))
		}
		fmt.Fprint(w, "data: {\"heartbeat\":true}\n\n")
	})
}

// resourceState is the statuses a test streams, which its calls change.
type resourceState struct {
	mu       sync.Mutex
	statuses []resourceStatus
}

func (st *resourceState) get() []resourceStatus {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.statuses
}

func (st *resourceState) set(statuses ...resourceStatus) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.statuses = statuses
}

// resourceNotices streams statuses, which the test may change.
func (s *shelley) resourceNotices(statuses ...resourceStatus) *resourceState {
	st := &resourceState{statuses: statuses}
	s.resourceStream(st.get)
	return st
}

// resourceDismiss answers POST /api/resources/{kind}/dismiss as the server
// would, with the status dismissed gives, after recording the request's
// path and the episode_id its body names; call "dismissals" lists them, one
// "<path> episode <id>" per line.
func (s *shelley) resourceDismiss(dismissed func(kind string) resourceStatus) {
	var mu sync.Mutex
	var got []string
	s.route("POST /api/resources/{kind}/dismiss", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Episode any `json:"episode_id"`
		}
		json.Unmarshal(readBody(r), &body)
		mu.Lock()
		got = append(got, fmt.Sprintf("%s episode %v", r.URL.Path, body.Episode))
		mu.Unlock()
		writeJSON(w, dismissed(r.PathValue("kind")))
	})
	s.call("dismissals", func() string {
		mu.Lock()
		defer mu.Unlock()
		if len(got) == 0 {
			return "none"
		}
		out := ""
		for i, g := range got {
			if i > 0 {
				out += "\n"
			}
			out += g
		}
		return out
	})
}

func TestE2EResourceNoticeDiskDismissesEpisode(t *testing.T) {
	e2e(t, `Resource notices: shows disk status and dismisses the current episode. The page's live stream (/api/stream2) reports low disk space (episode 7, 1.6 GB available), and dismissing it is faked; call "dismissals" lists every dismiss request as "<path> episode <episode_id in its body>", one per line, or "none". Perform the steps in order. 1. Navigate to /. 2. The element with data-testid "disk-notice" (the notice) should be visible (allow 30s). 3. The notice's text should contain "Disk space is low". 4. The notice's text should contain "1.6 GB remaining". 5. The element with data-testid "disk-notice-title" must have the attribute role exactly "status". 6. Call "dismissals": it must return exactly "none". 7. Click the element with data-testid "disk-notice-dismiss". 8. Call "dismissals" until it returns exactly "/api/resources/disk/dismiss episode 7". 9. There must then be exactly 0 elements with data-testid "disk-notice".`, func(s *shelley) {
		st := s.resourceNotices(lowDisk)
		s.resourceDismiss(func(string) resourceStatus {
			dismissed := lowDisk.with(resourceStatus{"revision": 4, "dismissed": true})
			st.set(dismissed)
			return dismissed
		})
	})
}

func TestE2EResourceNoticeExeDevResizeLink(t *testing.T) {
	e2e(t, `Resource notices link disk, not memory, to an exe.dev resize on exe.dev hosts. The page believes it runs on exe.dev as host myvm.exe.xyz, and its live stream (/api/stream2) reports both low disk space and high memory pressure. Perform the steps in order. 1. Navigate to /. 2. The element with data-testid "disk-notice-link" should have exactly the text "Resize this VM" (allow 30s). 3. Its href attribute must be exactly "https://exe.dev/suggest?command=resize%20myvm%20--disk%3D90GB". 4. The element with data-testid "memory-notice" should be visible. 5. There must be exactly 0 elements with data-testid "memory-notice-link".`, func(s *shelley) {
		s.patchInit(map[string]any{"is_exe_dev": true, "hostname": "myvm.exe.xyz"})
		s.resourceNotices(lowDisk, highMemory)
	})
}

func TestE2EResourceNoticeCriticalDiskAlert(t *testing.T) {
	e2e(t, `Resource notices announce critical disk status as an alert. The page's live stream (/api/stream2) reports critically low disk space. Perform the steps in order. 1. Navigate to /. 2. The element with data-testid "disk-notice" should be visible (allow 30s). 3. Its text should contain "Disk space is critically low". 4. The element with data-testid "disk-notice-title" must have the attribute role exactly "alert".`, func(s *shelley) {
		s.resourceNotices(lowDisk.with(resourceStatus{"critical": true, "available_bytes": 400_000_000}))
	})
}

func TestE2EResourceNoticeMemoryBesideDisk(t *testing.T) {
	e2e(t, `Resource notices show memory pressure beside disk and dismiss it alone. The page's live stream (/api/stream2) reports low disk space and high memory pressure (1.2 GB available, 41.7% stalled), and dismissing a notice is faked; call "dismissals" lists every dismiss request as "<path> episode <episode_id in its body>", one per line, or "none". Perform the steps in order. 1. Navigate to /. 2. The element with data-testid "memory-notice" (the notice) should be visible (allow 30s). 3. The notice's text should contain "Memory pressure is high". 4. It should contain "1.2 GB available". 5. It should contain "42% stalled". 6. The element with data-testid "memory-notice-title" must have the attribute role exactly "status". 7. Call "dismissals": it must return exactly "none". 8. Click the element with data-testid "memory-notice-dismiss". 9. Call "dismissals" until it returns exactly "/api/resources/memory/dismiss episode 2". 10. There must then be exactly 0 elements with data-testid "memory-notice". 11. The element with data-testid "disk-notice" should still be visible.`, func(s *shelley) {
		st := s.resourceNotices(lowDisk, highMemory)
		s.resourceDismiss(func(kind string) resourceStatus {
			dismissed := highMemory.with(resourceStatus{"revision": 6, "dismissed": true})
			st.set(lowDisk, dismissed)
			return dismissed
		})
	})
}

func TestE2EResourceNoticeCriticalMemoryAlert(t *testing.T) {
	e2e(t, `Resource notices announce critical memory pressure as an alert. The page's live stream (/api/stream2) reports critical memory pressure with 300 MB available. Perform the steps in order. 1. Navigate to /. 2. The element with data-testid "memory-notice" should be visible (allow 30s). 3. Its text should contain "Memory pressure is critical". 4. It should contain "300 MB available". 5. The element with data-testid "memory-notice-title" must have the attribute role exactly "alert".`, func(s *shelley) {
		s.resourceNotices(highMemory.with(resourceStatus{"critical": true, "available_bytes": 300_000_000}))
	})
}

// E.g. a restarted server whose memory monitor could not start.
func TestE2EResourceNoticeDropsUnreportedKind(t *testing.T) {
	e2e(t, `Resource notices drop a notice whose kind a reconnect no longer reports. The page's live stream (/api/stream2) reports low disk space and high memory pressure; each connection ends at once and the page reconnects about a second later. Call "drop memory" makes later connections report only the low disk space, as a restarted server whose memory monitor could not start would. Perform the steps in order. 1. Navigate to /. 2. The element with data-testid "memory-notice" should be visible (allow 30s). 3. Call "drop memory". 4. There should then be exactly 0 elements with data-testid "memory-notice" (allow 30s). 5. The element with data-testid "disk-notice" should be visible.`, func(s *shelley) {
		st := s.resourceNotices(lowDisk, highMemory)
		s.call("drop memory", func() string {
			st.set(lowDisk)
			return "dropped"
		})
	})
}

// Kernels without PSI report zero, as does memory that is short but not yet
// contended.
func TestE2EResourceNoticeOmitsZeroStall(t *testing.T) {
	e2e(t, `Resource notices omit the stall share when nothing is stalled. The page's live stream (/api/stream2) reports high memory pressure with 1.2 GB available and a stall share of only 0.4%. Perform the steps in order. 1. Navigate to /. 2. The text of the element with data-testid "memory-notice" should contain "1.2 GB available" (allow 30s). 3. Its text must not contain "stalled".`, func(s *shelley) {
		s.resourceNotices(highMemory.with(resourceStatus{"stall_percent": 0.4}))
	})
}
