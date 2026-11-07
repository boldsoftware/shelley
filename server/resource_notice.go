package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"
	"shelley.exe.dev/server/resource"
	"shelley.exe.dev/subpub"
)

// Each kind persists its episode under its own internal setting.
const (
	diskSpaceSettingKey = "internal.disk_space"
	memorySettingKey    = "internal.memory"
)

var resourceSettingKeys = map[resource.Kind]string{
	resource.Disk:   diskSpaceSettingKey,
	resource.Memory: memorySettingKey,
}

// resourceRecheckInterval paces re-checks while a notice is active.
const resourceRecheckInterval = 30 * time.Second

// Persist only episode transitions and dismissal, never periodic measurements.
type resourceEpisode struct {
	EpisodeID uint64 `json:"episode_id"`
	Revision  uint64 `json:"revision"`
	Active    bool   `json:"active"`
	Critical  bool   `json:"critical"`
	Dismissed bool   `json:"dismissed"`
}

// resourceReading is one successful probe of a resource.
type resourceReading struct {
	available, total uint64
	// someStall and fullStall are PSI avg60 percentages, memory only.
	someStall, fullStall float64
}

// classify reports whether a reading is low and whether it is critically low.
// active is whether an episode is under way; memory needs a wider margin to
// end one (see resource.MemoryLowPercent).
func classify(kind resource.Kind, r resourceReading, active bool) (low, critical bool) {
	if kind == resource.Disk {
		return r.available < resource.DiskThreshold, r.available < resource.DiskCriticalThreshold
	}
	line := r.total * resource.MemoryLowPercent / 100
	if line > resource.MemoryLowCap {
		line = resource.MemoryLowCap
	}
	critical = r.available < line/2 || r.fullStall > resource.MemoryStallPercent
	if active {
		return r.available < line*3/2 || r.someStall > resource.MemoryStallPercent/2, critical
	}
	return r.available < line || r.someStall > resource.MemoryStallPercent, critical
}

type resourceMonitor struct {
	mu     sync.Mutex // serializes observations, persistence, publishing and subscription
	server *Server
	kind   resource.Kind
	probe  func() (resourceReading, error)
	status resource.Status
	// unsaved means status is newer than the persisted episode, typically
	// because the disk was too full to write it. Every check retries.
	unsaved bool
	// poking guards the single background check queued by poke.
	poking atomic.Bool
	// While a notice is active, recheck checks once nothing else has sampled
	// for recheckEvery: an idle Shelley would otherwise keep showing pressure
	// long gone. Zero (tests) disables it. Both require mu.
	recheckEvery time.Duration
	recheck      *time.Timer
}

func readDisk(path string) (resourceReading, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return resourceReading{}, fmt.Errorf("statfs %q: %w", path, err)
	}
	return resourceReading{available: stat.Bavail * uint64(stat.Bsize), total: stat.Blocks * uint64(stat.Bsize)}, nil
}

// initResourceMonitors runs synchronously before serving: the database
// filesystem everywhere, memory where the kernel reports it (Linux).
func (s *Server) initResourceMonitors(ctx context.Context) error {
	s.resourceRecheckEvery = resourceRecheckInterval
	if err := s.initResourceMonitor(ctx, resource.Disk, func() (resourceReading, error) { return readDisk(s.db.Path()) }); err != nil {
		return fmt.Errorf("initialize disk space monitor: %w", err)
	}
	if runtime.GOOS == "linux" {
		// The notice is advisory: unreadable memory files must not stop Shelley.
		if err := s.initResourceMonitor(ctx, resource.Memory, readMemory); err != nil {
			s.logger.Warn("Memory pressure notice disabled", "error", err)
		}
	}
	return nil
}

// initResourceMonitor loads kind's episode and takes the first reading.
// Route-only tests can inject a probe here and call check directly, without a
// worker or any sleeps.
func (s *Server) initResourceMonitor(ctx context.Context, kind resource.Kind, probe func() (resourceReading, error)) error {
	value, err := s.db.GetSetting(ctx, resourceSettingKeys[kind])
	if err != nil {
		return fmt.Errorf("load %s episode: %w", kind, err)
	}
	var episode resourceEpisode
	if value != "" {
		if err := json.Unmarshal([]byte(value), &episode); err != nil {
			return fmt.Errorf("decode %s episode: %w", kind, err)
		}
	}
	m := &resourceMonitor{
		server: s,
		kind:   kind,
		probe:  probe,
		status: resource.Status{
			Kind:      kind,
			EpisodeID: episode.EpisodeID,
			Revision:  episode.Revision,
			Active:    episode.Active,
			Critical:  episode.Critical,
			Dismissed: episode.Dismissed,
		},
	}
	if err := m.check(ctx); err != nil {
		return err
	}
	s.resourceMonitorSlot(kind).Store(m)
	// Only now: a monitor whose init failed must not live on in a timer.
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recheckEvery = s.resourceRecheckEvery
	m.scheduleRecheck()
	return nil
}

// resourceMonitorSlot is nil for an unknown kind.
func (s *Server) resourceMonitorSlot(kind resource.Kind) *atomic.Pointer[resourceMonitor] {
	switch kind {
	case resource.Disk:
		return &s.diskSpace
	case resource.Memory:
		return &s.memory
	}
	return nil
}

// resourceMonitors lists the initialized monitors in display order.
func (s *Server) resourceMonitors() []*resourceMonitor {
	var monitors []*resourceMonitor
	for _, kind := range resource.Kinds {
		if m := s.resourceMonitorSlot(kind).Load(); m != nil {
			monitors = append(monitors, m)
		}
	}
	return monitors
}

// onDiskFull runs when a database operation fails because the disk is full.
// The failure is the most timely signal there is, so re-check right away
// rather than waiting for the next recorded message or page load.
func (s *Server) onDiskFull() {
	if m := s.diskSpace.Load(); m != nil {
		m.poke()
	}
}

// poke checks in the background: the hook runs on the failing caller's
// goroutine, possibly while check itself holds mu (its own write can fail
// too). At most one poke runs at a time; extra pokes are dropped.
func (m *resourceMonitor) poke() {
	if !m.poking.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer m.poking.Store(false)
		m.backgroundCheck()
	}()
}

// backgroundCheck runs for poke and the re-check timer, until shutdown.
func (m *resourceMonitor) backgroundCheck() {
	select {
	case <-m.server.shutdownCh:
		return
	default:
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := m.check(ctx); err != nil {
		m.server.logger.Error("Resource check failed", "kind", m.kind, "error", err)
	}
}

func (m *resourceMonitor) snapshot() resource.Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

func (m *resourceMonitor) check(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	defer m.scheduleRecheck()
	reading, err := m.probe()
	if err != nil {
		// Unknown is not recovery: retain the last successful observation.
		return err
	}
	low, critical := classify(m.kind, reading, m.status.Active)
	next := m.status
	next.AvailableBytes = reading.available
	next.TotalBytes = reading.total
	next.StallPercent = reading.someStall
	next.Active = low
	// Critical latches within an episode: hovering around the line must not
	// re-show the notice, so only recovery clears it.
	next.Critical = low && (m.status.Critical || critical)
	if next.Active == m.status.Active && next.Critical == m.status.Critical {
		m.status = next
		// Visible notice: push the fresh numbers. Same revision, nothing persisted.
		if next.Active && !next.Dismissed {
			m.server.streamPub.Broadcast(StreamResponse{ResourceStatus: &next})
		}
	} else {
		// Entering low, entering critical or recovering each un-dismiss: the
		// user gets one fresh notice per escalation.
		next.Dismissed = false
		if next.Active && !m.status.Active {
			next.EpisodeID++
		}
		next.Revision++
		// Publish before persisting: a full disk is exactly when the write
		// fails, and that must not hide the notice saying so.
		m.status = next
		m.unsaved = true
		m.server.streamPub.Broadcast(StreamResponse{ResourceStatus: &next})
	}
	if m.unsaved {
		if err := m.persist(ctx, m.status); err != nil {
			// Not fatal: the persisted episode only matters across restarts.
			m.server.logger.Error("Failed to persist resource notice", "kind", m.kind, "error", err)
		} else {
			m.unsaved = false
		}
	}
	return nil
}

// scheduleRecheck requires mu. Every check, failed or not, pushes the
// recheck out, so it only fires when nothing else samples.
func (m *resourceMonitor) scheduleRecheck() {
	switch {
	case m.recheckEvery == 0:
	case !m.status.Active:
		if m.recheck != nil {
			m.recheck.Stop()
		}
	case m.recheck == nil:
		// Not poke: a poke still finishing its check would drop this one.
		m.recheck = time.AfterFunc(m.recheckEvery, m.backgroundCheck)
	default:
		m.recheck.Reset(m.recheckEvery)
	}
}

// persist requires mu.
func (m *resourceMonitor) persist(ctx context.Context, status resource.Status) error {
	value, err := json.Marshal(resourceEpisode{
		EpisodeID: status.EpisodeID,
		Revision:  status.Revision,
		Active:    status.Active,
		Critical:  status.Critical,
		Dismissed: status.Dismissed,
	})
	if err != nil {
		return err
	}
	if err := m.server.db.SetSetting(ctx, resourceSettingKeys[m.kind], string(value)); err != nil {
		return fmt.Errorf("persist %s episode: %w", m.kind, err)
	}
	return nil
}

// dismiss is durable or fails: the user is told when it did not stick.
func (m *resourceMonitor) dismiss(ctx context.Context, episodeID uint64) (resource.Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.status.Active && !m.status.Dismissed && m.status.EpisodeID == episodeID {
		next := m.status
		next.Dismissed = true
		next.Revision++
		if err := m.persist(ctx, next); err != nil {
			return m.status, err
		}
		m.status = next
		m.unsaved = false
		m.server.streamPub.Broadcast(StreamResponse{ResourceStatus: &next})
	}
	return m.status, nil
}

// refreshResources runs whenever an agent loop records a message: tool
// results follow exactly the work that fills disks and memory, and memory
// pressure fades within a minute or two, so sampling only at turn end could
// miss it. Together with the startup check and each new stream this is the
// only sampling besides re-checks of active notices; per monitor one cheap
// probe, no DB write unless a threshold was crossed.
func (s *Server) refreshResources(ctx context.Context) {
	for _, m := range s.resourceMonitors() {
		if err := m.check(ctx); err != nil {
			s.logger.Error("Resource check failed", "kind", m.kind, "error", err)
		}
	}
}

// Sample once per new stream (a page load or reconnect), so someone opening an
// idle Shelley sees the current state, then subscribe and capture the
// snapshots under the publishing locks. Any transition the sample caused was
// broadcast before this subscriber existed, so it arrives only via the
// snapshot; events queued afterwards are never older than it.
func (s *Server) subscribeStream(ctx context.Context) (func() (StreamResponse, bool), *subpub.SubscriptionStatus, []resource.Status) {
	s.refreshResources(ctx)
	var snapshots []resource.Status
	for _, m := range s.resourceMonitors() {
		m.mu.Lock()
		defer m.mu.Unlock()
		snapshots = append(snapshots, m.status)
	}
	next, status := s.streamPub.SubscribeWithStatus(ctx, -1)
	return next, status, snapshots
}

func (s *Server) handleDismissResource(w http.ResponseWriter, r *http.Request) {
	kind := resource.Kind(r.PathValue("kind"))
	var m *resourceMonitor
	if slot := s.resourceMonitorSlot(kind); slot != nil {
		m = slot.Load()
	}
	if m == nil {
		http.Error(w, fmt.Sprintf("No %q resource monitor", kind), http.StatusNotFound)
		return
	}
	var req struct {
		EpisodeID uint64 `json:"episode_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.EpisodeID == 0 {
		http.Error(w, "A positive episode_id is required", http.StatusBadRequest)
		return
	}
	status, err := m.dismiss(r.Context(), req.EpisodeID)
	if err != nil {
		s.internalError(w, "Failed to dismiss resource notice", err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}
