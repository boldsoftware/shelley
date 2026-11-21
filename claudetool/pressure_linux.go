package claudetool

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// resourceNotes notes in a local command's output what the command cannot
// see: processes the kernel's OOM killer killed while it ran, resource
// pressure, and a nearly full root filesystem.
type resourceNotes struct {
	// proc and kmsg are where procfs and the kernel log are.
	proc, kmsg string
	statfs     func(path string, st *unix.Statfs_t) error
}

func newResourceNotes() *resourceNotes {
	return &resourceNotes{proc: "/proc", kmsg: "/dev/kmsg", statfs: unix.Statfs}
}

// psiThresholds are the PSI "some avg10", the share (percent) of the last
// 10s that some tasks stalled, at or above which a note mentions a
// resource. A healthy machine stays near 0. CPU is left out: builds
// saturate it as a matter of course.
var psiThresholds = []struct {
	resource string
	some     float64
}{{"memory", 10}, {"io", 30}}

// maxVictims is how many OOM killer victims a note names.
const maxVictims = 3

// resourceWatch collects, while a command runs, the OOM killer's victims.
type resourceWatch struct {
	notes *resourceNotes
	// pid is the command's wrapper, which leads its own process group.
	pid int
	// stop's closing stops collect; it is -1 if collect did not start.
	stop    int
	done    chan struct{}
	victims []oomVictim // set once done is closed
}

// watch starts watching for a command whose wrapper, just started in a
// process group of its own, is pid; n may be nil.
func (n *resourceNotes) watch(pid int) *resourceWatch {
	if n == nil {
		return nil
	}
	w := &resourceWatch{notes: n, pid: pid, stop: -1, done: make(chan struct{})}
	var pipe [2]int
	kmsg, err := unix.Open(n.kmsg, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err == nil {
		if err = unix.Pipe2(pipe[:], unix.O_CLOEXEC); err != nil {
			unix.Close(kmsg)
		}
	}
	if err != nil {
		close(w.done)
		return w
	}
	unix.Seek(kmsg, 0, io.SeekEnd) // only records from now on
	w.stop = pipe[1]
	go w.collect(kmsg, pipe[0])
	return w
}

// collect reads the OOM killer's victims from the kernel log as it logs
// them, while they can still be looked up in /proc, until the other
// end of stop is closed.
func (w *resourceWatch) collect(kmsg, stop int) {
	defer close(w.done)
	defer unix.Close(kmsg)
	defer unix.Close(stop)
	fds := []unix.PollFd{{Fd: int32(kmsg), Events: unix.POLLIN}, {Fd: int32(stop), Events: unix.POLLIN}}
	buf := make([]byte, 8192)
	for {
		for range 10000 { // a bound, for records keep coming in an OOM storm
			n, err := unix.Read(kmsg, buf)
			if err == unix.EPIPE {
				continue // records were overwritten; reading resumes at the oldest
			}
			if err != nil || n <= 0 {
				break // EAGAIN: no more records
			}
			for record := range strings.Lines(string(buf[:n])) {
				if v, ok := parseOOMVictim(record); ok {
					v.outside = w.outside(v.pid)
					w.victims = append(w.victims, v)
				}
			}
		}
		if fds[1].Revents != 0 {
			return
		}
		if _, err := unix.Poll(fds, -1); err != nil && err != unix.EINTR {
			return
		}
	}
}

// finish returns, once the command exited, its note, or "" when nothing
// is unusual.
func (w *resourceWatch) finish() string {
	if w == nil {
		return ""
	}
	if w.stop >= 0 {
		unix.Close(w.stop)
	}
	<-w.done
	var note []string
	victims := w.victims
	if len(victims) > 0 {
		var vs []string
		for _, v := range victims[:min(len(victims), maxVictims)] {
			outside := ""
			if v.outside {
				outside = ", outside this command"
			}
			vs = append(vs, fmt.Sprintf("%q (pid %d, %s%s)", v.name, v.pid, formatBytes(v.rss), outside))
		}
		if len(victims) > maxVictims {
			vs = append(vs, fmt.Sprintf("and %d more", len(victims)-maxVictims))
		}
		note = append(note, "The kernel's OOM killer killed "+strings.Join(vs, ", ")+" while this ran.")
	}
	var stalls []string
	memory := len(victims) > 0
	for _, t := range psiThresholds {
		some, ok := readPSI(filepath.Join(w.notes.proc, "pressure", t.resource))
		if !ok || some < t.some {
			continue
		}
		stalls = append(stalls, fmt.Sprintf("%s %.0f%%", t.resource, some))
		memory = memory || t.resource == "memory"
	}
	if len(stalls) > 0 {
		note = append(note, "Resource pressure (PSI some avg10): "+strings.Join(stalls, ", ")+".")
	}
	if memory {
		if total, avail, ok := readMeminfo(filepath.Join(w.notes.proc, "meminfo")); ok {
			note = append(note, fmt.Sprintf("Memory available: %s of %s.", formatBytes(avail), formatBytes(total)))
		}
	}
	var st unix.Statfs_t
	if w.notes.statfs("/", &st) == nil && st.Bavail*uint64(st.Bsize) < 256<<20 {
		note = append(note, fmt.Sprintf("Disk nearly full: / has %s free of %s.", formatBytes(st.Bavail*uint64(st.Bsize)), formatBytes(st.Blocks*uint64(st.Bsize))))
	}
	if len(note) == 0 {
		return ""
	}
	return "[shelley: " + strings.Join(note, " ") + "]"
}

// oomVictim is a process the kernel's OOM killer killed.
type oomVictim struct {
	pid  int
	name string
	rss  uint64 // bytes
	// outside is whether it was known not to be the command's.
	outside bool
}

// parseOOMVictim parses a kernel log record of the OOM killer's, such as
//
//	3,1291,48518832,-;Out of memory: Killed process 439 (leak) total-vm:9163812kB, anon-rss:7906176kB, file-rss:1224kB, shmem-rss:0kB, ...
func parseOOMVictim(record string) (oomVictim, bool) {
	_, rest, ok := strings.Cut(record, ": Killed process ")
	if !ok {
		return oomVictim{}, false
	}
	pid, rest, _ := strings.Cut(rest, " (")
	i := strings.LastIndex(rest, ") total-vm:") // the name may hold parentheses
	n, err := strconv.Atoi(pid)
	if i < 0 || err != nil {
		return oomVictim{}, false
	}
	v := oomVictim{pid: n, name: rest[:i]}
	for _, field := range []string{"anon-rss:", "file-rss:", "shmem-rss:"} {
		v.rss += kbField(rest[i:], field) << 10
	}
	return v, true
}

// outside reports whether pid is known not to be the command's: neither
// it nor a process it descends from is in the wrapper's process group.
// The group catches orphans, as in (leak &); the descent catches commands
// that make their own group, as timeout does.
func (w *resourceWatch) outside(pid int) bool {
	for pid > 1 {
		b, err := os.ReadFile(filepath.Join(w.notes.proc, strconv.Itoa(pid), "stat"))
		if err != nil {
			return false // gone already: unknown
		}
		// pid (comm) state ppid pgrp ...; comm may hold spaces and parentheses.
		f := strings.Fields(string(b[bytes.LastIndexByte(b, ')')+1:]))
		if len(f) < 3 || f[2] == strconv.Itoa(w.pid) {
			return false
		}
		if pid, err = strconv.Atoi(f[1]); err != nil {
			return false
		}
	}
	return true
}

// readPSI returns the "some avg10" of a /proc/pressure file.
func readPSI(path string) (float64, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	var some float64
	_, err = fmt.Sscanf(string(b), "some avg10=%f", &some)
	return some, err == nil
}

// readMeminfo returns MemTotal and MemAvailable in bytes.
func readMeminfo(path string) (total, avail uint64, ok bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, false
	}
	total, avail = kbField(string(b), "MemTotal:")<<10, kbField(string(b), "MemAvailable:")<<10
	return total, avail, total > 0
}

// kbField returns N from "<name>NkB" in s, or 0.
func kbField(s, name string) uint64 {
	_, rest, ok := strings.Cut(s, name)
	if !ok {
		return 0
	}
	rest = strings.TrimLeft(rest, " ")
	n, _ := strconv.ParseUint(rest[:strings.IndexFunc(rest+"k", func(r rune) bool { return r < '0' || r > '9' })], 10, 64)
	return n
}

func formatBytes(n uint64) string {
	if n < 1<<20 {
		return fmt.Sprintf("%d KB", n>>10)
	}
	if n < 1<<30 {
		return fmt.Sprintf("%d MB", n>>20)
	}
	return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
}
