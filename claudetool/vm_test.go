package claudetool

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	"shelley.exe.dev/llm"
)

// localVM is a VM that is this machine, in dir, with VIA_VM=1 set. Like
// ssh, it relays stdin to a command in another session, which sees EOF
// once the relay dies.
type localVM struct {
	dir      string
	prepares *atomic.Int32
}

const localVMRelay = `d=$(mktemp -d) && mkfifo "$d/in" || exit 1
cat <&0 >"$d/in" & c=$!
script=$1; shift
setsid env VIA_VM=1 bash -c "$script" shelley "$@" <"$d/in"
s=$?
kill $c 2>/dev/null; rm -rf -- "$d"
exit $s`

func (v localVM) Name() string { return "box" }
func (v localVM) Dir() string  { return v.dir }
func (v localVM) Prepare(context.Context) error {
	if v.prepares != nil {
		v.prepares.Add(1)
	}
	return nil
}
func (v localVM) Command(script string, args ...string) []string {
	return append([]string{"bash", "-c", localVMRelay, "relay", script}, args...)
}

func TestVMTargets(t *testing.T) {
	t.Parallel()
	here, there := t.TempDir(), t.TempDir()
	tools := map[string]*llm.Tool{}
	jobs := make(testJobs, 1)
	prepares := new(atomic.Int32)
	for _, tool := range NewToolSet(t.Context(), ToolSetConfig{WorkingDir: here, VM: localVM{there, prepares}, BackgroundJobs: jobs}).Tools() {
		tools[tool.Name] = tool
	}
	for _, name := range []string{"bash", "patch"} {
		var schema struct{ Required []string }
		if err := json.Unmarshal(tools[name].InputSchema, &schema); err != nil || !strings.Contains(strings.Join(schema.Required, ","), "ssh_target") {
			t.Fatalf("%s schema: %s", name, tools[name].InputSchema)
		}
	}
	call := func(tool, input string) llm.ToolOut {
		t.Helper()
		return tools[tool].Run(t.Context(), json.RawMessage(input))
	}
	if out := call("bash", `{"command":"printf '%s|%s' \"$VIA_VM\" \"$PWD\"","ssh_target":"box"}`); out.Error != nil || out.LLMContent[0].Text != "1|"+there {
		t.Fatalf("bash on the VM: %v %+v", out.Error, out.LLMContent)
	} else if display := out.Display.(BashDisplayData); display.WorkingDir != "box:"+there {
		t.Fatalf("display: %+v", display)
	}
	if out := call("bash", `{"command":"printf '%s|%s' \"$VIA_VM\" \"$PWD\"","ssh_target":"localhost"}`); out.Error != nil || out.LLMContent[0].Text != "|"+here {
		t.Fatalf("bash here: %v %+v", out.Error, out.LLMContent)
	}
	if out := call("bash", `{"command":"true"}`); out.Error == nil || !strings.Contains(out.Error.Error(), "ssh_target is required") {
		t.Fatalf("no ssh_target: %v", out.Error)
	}
	for _, target := range []string{"guest", ""} {
		if out := call("bash", `{"command":"true","ssh_target":"`+target+`"}`); out.Error == nil || !strings.Contains(out.Error.Error(), `ssh_target "`+target+`" is neither "box" nor "localhost"`) {
			t.Fatalf("unknown ssh_target %q: %v", target, out.Error)
		}
	}
	if out := call("bash", `{"command":"exit 3","ssh_target":"box"}`); out.Error == nil || !strings.Contains(out.Error.Error(), "exit status 3") {
		t.Fatalf("failing VM command: %v", out.Error)
	}

	// Killing a background job here kills its command there: the command
	// holds a FIFO open, whose reader sees EOF once it is gone.
	ready := filepath.Join(there, "ready")
	if err := syscall.Mkfifo(ready, 0o600); err != nil {
		t.Fatal(err)
	}
	out := call("bash", `{"command":"exec sleep 300 3>ready","ssh_target":"box","background":true}`)
	if out.Error != nil {
		t.Fatal(out.Error)
	}
	fifo, err := os.Open(ready) // returns once the command holds it
	if err != nil {
		t.Fatal(err)
	}
	defer fifo.Close()
	if out := call("bash", `{"command":"kill -- -`+strconv.Itoa(out.Display.(BashDisplayData).Background.PGID)+`","ssh_target":"localhost"}`); out.Error != nil {
		t.Fatal(out.Error)
	}
	<-jobs
	if _, err := io.ReadAll(fifo); err != nil {
		t.Fatal(err)
	}

	if out := call("patch", `{"path":"a/notes.txt","ssh_target":"box","patches":[{"operation":"overwrite","newText":"one\n"}]}`); out.Error != nil {
		t.Fatal(out.Error)
	} else if display := out.Display.(PatchDisplayData); display.Path != "box:"+filepath.Join(there, "a/notes.txt") {
		t.Fatalf("display: %+v", display)
	}
	// A VM patch prepares the VM once, and replaces the file whole: through
	// a symlink, keeping its mode, leaving nothing else behind.
	if err := os.Chmod(filepath.Join(there, "a/notes.txt"), 0o751); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("notes.txt", filepath.Join(there, "a/link")); err != nil {
		t.Fatal(err)
	}
	prepares.Store(0)
	if out := call("patch", `{"path":"`+there+`/a/link","ssh_target":"box","patches":[{"operation":"replace","oldText":"one","newText":"two"}]}`); out.Error != nil {
		t.Fatal(out.Error)
	}
	if n := prepares.Load(); n != 1 {
		t.Fatalf("a VM patch prepared the VM %d times", n)
	}
	if data, err := os.ReadFile(filepath.Join(there, "a/notes.txt")); err != nil || string(data) != "two\n" {
		t.Fatalf("VM file: %q %v", data, err)
	}
	if info, err := os.Stat(filepath.Join(there, "a/notes.txt")); err != nil || info.Mode().Perm() != 0o751 {
		t.Fatalf("VM file mode: %v %v", info.Mode(), err)
	}
	if entries, err := os.ReadDir(filepath.Join(there, "a")); err != nil || len(entries) != 2 {
		t.Fatalf("VM directory: %v %v", entries, err)
	}
	if out := call("patch", `{"path":"missing.txt","ssh_target":"box","patches":[{"operation":"replace","oldText":"x","newText":"y"}]}`); out.Error == nil {
		t.Fatal("replace in a missing VM file succeeded")
	}
	if out := call("patch", `{"path":"notes.txt","ssh_target":"localhost","patches":[{"operation":"overwrite","newText":"here\n"}]}`); out.Error != nil {
		t.Fatal(out.Error)
	}
	if _, err := os.Stat(filepath.Join(here, "notes.txt")); err != nil {
		t.Fatal(err)
	}
}

func TestNoVMNoTarget(t *testing.T) {
	t.Parallel()
	for _, tool := range NewToolSet(t.Context(), ToolSetConfig{WorkingDir: t.TempDir()}).Tools() {
		if strings.Contains(string(tool.InputSchema), `"ssh_target"`) {
			t.Fatalf("%s offers an ssh_target without a VM", tool.Name)
		}
		if tool.Name == "bash" {
			if out := tool.Run(t.Context(), json.RawMessage(`{"command":"true","ssh_target":"box"}`)); out.Error == nil {
				t.Fatal("bash accepted an ssh_target without a VM")
			}
		}
	}
}

type testJobs chan BackgroundJob

func (j testJobs) Background(_ context.Context, job BackgroundJob, exited <-chan struct{}) error {
	go func() {
		<-exited
		j <- job
	}()
	return nil
}
