package claudetool

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
	"shelley.exe.dev/llm"
)

// Records as /dev/kmsg returned them on exe.dev VMs, one per read.
var oomRecords = []string{
	"4,1227,48518288,-;kswapd0 invoked oom-killer: gfp_mask=0xcc0(GFP_KERNEL), order=0, oom_score_adj=0\n",
	"6,1290,48518790,-;oom-kill:constraint=CONSTRAINT_NONE,nodemask=(null),cpuset=/,mems_allowed=0,global_oom,task_memcg=/system.slice/shelley.service,task=leak,pid=439,uid=1000\n",
	"3,1291,48518832,-;Out of memory: Killed process 439 (leak) total-vm:9163812kB, anon-rss:7906176kB, file-rss:1224kB, shmem-rss:0kB, UID:1000 pgtables:15548kB oom_score_adj:-1\n SUBSYSTEM=memory\n",
	"6,1292,48600000,-;oom_reaper: reaped process 439 (leak), now anon-rss:0kB, file-rss:0kB, shmem-rss:0kB\n",
	"3,1401,98518832,-;Memory cgroup out of memory: Killed process 512 (my (odd) name) total-vm:1000kB, anon-rss:1048576kB, file-rss:0kB, shmem-rss:1048576kB, UID:1000 pgtables:15548kB oom_score_adj:0\n",
	"6,1402,98518840,-;Tasks in /user.slice/build.scope are going to be killed due to memory.oom.group set\n",
	"3,1403,98518850,-;Memory cgroup out of memory: Killed process 513 (cc1) total-vm:1000kB, anon-rss:1024kB, file-rss:0kB, shmem-rss:0kB, UID:1000 pgtables:15548kB oom_score_adj:0\n",
	"3,1405,99000000,-;Out of memory: Killed process 700 (leak2) total-vm:1000kB, anon-rss:1024kB, file-rss:0kB, shmem-rss:0kB, UID:1000 pgtables:15548kB oom_score_adj:0\n",
}

// fakeMachine is a procfs, kernel log, and root filesystem for resourceNotes.
type fakeMachine struct {
	t     *testing.T
	dir   string
	notes *resourceNotes
	disk  unix.Statfs_t
	// kmsg writes to the kernel log, a FIFO.
	kmsg *os.File
}

func newFakeMachine(t *testing.T) *fakeMachine {
	m := &fakeMachine{t: t, dir: t.TempDir()}
	m.notes = &resourceNotes{
		proc: filepath.Join(m.dir, "proc"),
		kmsg: filepath.Join(m.dir, "kmsg"),
		statfs: func(path string, st *unix.Statfs_t) error {
			*st = m.disk
			return nil
		},
	}
	m.setDisk(20<<30, 25<<30)
	if err := unix.Mkfifo(m.notes.kmsg, 0o600); err != nil {
		t.Fatal(err)
	}
	// Read-write, so opening it does not block and it never hangs up.
	kmsg, err := os.OpenFile(m.notes.kmsg, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { kmsg.Close() })
	m.kmsg = kmsg
	m.write(map[string]string{
		"proc/pressure/memory": psi(0),
		"proc/pressure/io":     psi(0),
		"proc/meminfo":         "MemTotal:        8118272 kB\nMemFree:  100 kB\nMemAvailable:    7340032 kB\n",
	})
	return m
}

func psi(some float64) string {
	return fmt.Sprintf("some avg10=%.2f avg60=0.00 avg300=0.00 total=0\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=0\n", some)
}

func (m *fakeMachine) write(files map[string]string) {
	for name, content := range files {
		path := filepath.Join(m.dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			m.t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			m.t.Fatal(err)
		}
	}
}

func (m *fakeMachine) setDisk(free, size uint64) {
	m.disk = unix.Statfs_t{Bsize: 4096, Blocks: size / 4096, Bavail: free / 4096}
}

// shell returns a shell command writing content to the fake file name.
func (m *fakeMachine) shell(op, name, content string) string {
	return fmt.Sprintf("printf '%%s' '%s' %s '%s'", content, op, filepath.Join(m.dir, name))
}

func TestResourceNote(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files map[string]string
		free  uint64
		want  string
	}{
		{name: "calm"},
		{
			name: "oom",
			files: map[string]string{
				"kmsg": strings.Join(oomRecords, ""),
				// Orphaned in the command's process group.
				"proc/439/stat": "439 (leak) R 1 4242 4242 0 -1\n",
				"proc/512/stat": "512 (my (odd) name) Z 1 512 512 0 -1\n",
				// Under timeout, which makes its own process group.
				"proc/513/stat":  "513 (cc1) R 600 600 4242 0 -1\n",
				"proc/600/stat":  "600 (timeout) S 4242 600 4242 0 -1\n",
				"proc/4242/stat": "4242 (bash) S 1 4242 4242 0 -1\n",
			},
			want: `The kernel's OOM killer killed "leak" (pid 439, 7.5 GB), "my (odd) name" (pid 512, 2.0 GB, outside this command), "cc1" (pid 513, 1 MB), and 1 more while this ran. ` +
				"Memory available: 7.0 GB of 7.7 GB.",
		},
		{
			name:  "pressure",
			files: map[string]string{"proc/pressure/cpu": psi(85), "proc/pressure/memory": psi(35), "proc/pressure/io": psi(29)},
			want:  "Resource pressure (PSI some avg10): memory 35%. Memory available: 7.0 GB of 7.7 GB.",
		},
		{name: "disk", free: 200 << 20, want: "Disk nearly full: / has 200 MB free of 25.0 GB."},
		{name: "disk ok", free: 256 << 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newFakeMachine(t)
			if tc.free != 0 {
				m.setDisk(tc.free, 25<<30)
			}
			w := m.notes.watch(4242)
			kmsg := tc.files["kmsg"]
			delete(tc.files, "kmsg")
			m.write(tc.files)
			m.kmsg.WriteString(kmsg)
			want := ""
			if tc.want != "" {
				want = "[shelley: " + tc.want + "]"
			}
			if got := w.finish(); got != want {
				t.Errorf("got  %s\nwant %s", got, want)
			}
		})
	}
}

func TestBashResourceNotes(t *testing.T) {
	m := newFakeMachine(t)
	jobs := make(recordingJobs, 1)
	tool := (&BashTool{WorkingDir: NewMutableWorkingDir(t.TempDir()), Jobs: jobs, resourceNotes: m.notes}).Tool()
	run := func(req bashInput) llm.ToolOut {
		t.Helper()
		input, err := json.Marshal(req)
		if err != nil {
			t.Fatal(err)
		}
		return tool.Run(t.Context(), input)
	}

	// The victim is in the command's process group.
	stat := filepath.Join(m.dir, "proc/439/stat")
	out := run(bashInput{Command: fmt.Sprintf(`mkdir -p %s && echo "439 (leak) R 1 $(cut -d' ' -f5 /proc/$$/stat) 0" > %s && `, filepath.Dir(stat), stat) +
		m.shell(">>", "kmsg", oomRecords[2]) + " && exit 137"})
	want := "[command failed: exit status 137]\n\n" +
		`[shelley: The kernel's OOM killer killed "leak" (pid 439, 7.5 GB) while this ran. Memory available: 7.0 GB of 7.7 GB.]` + "\n"
	if out.Error == nil || out.Error.Error() != want {
		t.Errorf("got  %v\nwant %s", out.Error, want)
	}

	if out := run(bashInput{Command: "echo hi"}); out.Error != nil || out.LLMContent[0].Text != "hi\n" {
		t.Errorf("got %v, %+v; want no note", out.Error, out.LLMContent)
	}

	if out := run(bashInput{Command: m.shell(">", "proc/pressure/io", psi(60)) + " && echo written", Background: true}); out.Error != nil {
		t.Fatal(out.Error)
	}
	bg := <-jobs
	bg.cleanup(t)
	waitClosed(t, bg.exited, "background job")
	want = "[shelley: Resource pressure (PSI some avg10): io 60%.]"
	if notice := bg.job.Outcome().Notice(); !strings.HasSuffix(notice, "\nwritten\n\n"+want) {
		t.Errorf("got  %s\nwant suffix %s", notice, want)
	}
}
