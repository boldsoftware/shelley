package claudetool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// VM is a machine a conversation works on besides this one, e.g. a
// project's exe.dev VM. The agent loop and every tool stay here; bash and
// patch calls then name their ssh_target, the VM's name or "localhost" for
// this machine, so the model decides each time where a command runs or a file
// lives.
type VM interface {
	// Name is the machine's SSH host name, the ssh_target that selects it.
	Name() string
	// Dir is the working directory there: commands start in it, and
	// relative paths resolve against it.
	Dir() string
	// Prepare makes the machine reachable, provisioning and connecting as
	// needed. Every tool call on it calls Prepare first and fails with its
	// error; nothing falls back to this machine.
	Prepare(ctx context.Context) error
	// Command returns the argv of a local process that runs
	// `bash -c script shelley args...` there, connected to the local
	// process's stdin, stdout and stderr, and exiting with its status.
	Command(script string, args ...string) []string
}

// Localhost is the ssh_target of this machine. A word rather than "":
// models garble empty-string arguments.
const Localhost = "localhost"

// targetVM resolves a call's ssh_target: the VM for its name, nil for
// Localhost. With a VM, ssh_target is required, so a call never lands on
// the wrong machine by default; without one it is not offered, and only
// Localhost, where the call runs anyway, is accepted.
func targetVM(vm VM, target *string) (VM, error) {
	switch {
	case vm == nil && (target == nil || *target == Localhost):
		return nil, nil
	case vm == nil:
		return nil, fmt.Errorf("ssh_target %q: this conversation has no VM; omit ssh_target", *target)
	case target != nil && *target == vm.Name():
		return vm, nil
	case target != nil && *target == Localhost:
		return nil, nil
	}
	choices := fmt.Sprintf("%q runs on %s (the project's code, in %s); %q runs here, on the Shelley host", vm.Name(), vm.Name(), vm.Dir(), Localhost)
	if target != nil {
		return nil, fmt.Errorf("ssh_target %q is neither %q nor %q: %s", *target, vm.Name(), Localhost, choices)
	}
	return nil, fmt.Errorf("ssh_target is required: %s", choices)
}

// targetSchema adds a required "ssh_target" property to a tool's JSON
// schema when the conversation has a VM.
func targetSchema(schema string, vm VM) string {
	if vm == nil {
		return schema
	}
	var s map[string]any
	if err := json.Unmarshal([]byte(schema), &s); err != nil {
		panic(err)
	}
	s["properties"].(map[string]any)["ssh_target"] = map[string]any{
		"type": "string",
		"enum": []string{vm.Name(), Localhost},
		"description": fmt.Sprintf("Which machine. %q: over ssh on %s, in %s, where the project's code is; use it for all project work. %q: here, on the Shelley host; only for Shelley's API, scp to or from %s, ssh -L to reach its ports from the browser, and files for read_image, output_iframe, or message_user.",
			vm.Name(), vm.Name(), vm.Dir(), Localhost, vm.Name()),
	}
	required, _ := s["required"].([]any)
	s["required"] = append(required, "ssh_target")
	out, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(out)
}

// VMJobScript runs a command on a VM. Arguments: working directory,
// command, then environment assignments. The command runs under
// bash --login in its own session, its output streamed back. When the local
// side goes away (stdin EOF), the session is killed, mirroring how a local
// job's process group dies with it, so the local process's stdin must stay
// open while it runs.
const VMJobScript = `wd=$1 cmd=$2; shift 2
export "$@"
cd -- "$wd" || exit 1
pg=$(mktemp) || exit 1
exec 3<&0 </dev/null
( cat <&3 >/dev/null
  for _ in $(seq 50); do [ -s "$pg" ] && break; sleep 0.1; done
  kill -KILL -- "-$(cat -- "$pg")" ) >/dev/null 2>&1 &
w=$!
exec 3<&-
setsid -w bash -c 'echo $$ >"$1" && exec bash --login -c "$2"' shelley "$pg" "$cmd" 2>&1
s=$?
kill "$w" 2>/dev/null
rm -f -- "$pg"
exit "$s"`

// vmBashJobWrapper is bashJobWrapper for a VM: it runs the argv after $1
// (VMJobScript there) and records its exit status in $1. The argv's stdin
// is a FIFO opened read-write, so it never sees EOF while this runs.
const vmBashJobWrapper = `trap : HUP INT TERM
e=$1; shift
mkfifo -m 600 -- "$e.in" || exit 1
"$@" <>"$e.in"
s=$?
rm -f -- "$e.in"
printf '%d\n' "$s" >"$e.tmp" && mv -f -- "$e.tmp" "$e"
exit "$s"`

// hostFS is the file access patch needs, on whichever machine holds the file.
type hostFS interface {
	ReadFile(ctx context.Context, path string) ([]byte, error)
	// WriteFile writes path, creating its directory: like os.WriteFile, a
	// new file gets perm and an existing one keeps its mode.
	WriteFile(ctx context.Context, path string, data []byte, perm fs.FileMode) error
}

func newHostFS(vm VM) hostFS {
	if vm == nil {
		return localFS{}
	}
	return vmFS{vm}
}

type localFS struct{}

func (localFS) ReadFile(_ context.Context, path string) ([]byte, error) { return os.ReadFile(path) }
func (localFS) WriteFile(_ context.Context, path string, data []byte, perm fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("failed to create directory %q: %w", filepath.Dir(path), err)
	}
	return os.WriteFile(path, data, perm)
}

// vmFS is a VM's files, over ssh. Its caller prepares the VM.
type vmFS struct{ vm VM }

// vmMissing is the exit status vmFS scripts use for a missing path.
const vmMissing = 44

func (v vmFS) run(ctx context.Context, op, path string, stdin []byte, script string, args ...string) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	argv := v.vm.Command(script, append([]string{path}, args...)...)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return stdout.Bytes(), nil
	case errors.As(err, &exitErr) && exitErr.ExitCode() == vmMissing:
		err = fs.ErrNotExist
	case errors.As(err, &exitErr):
		err = fmt.Errorf("on %s: %w: %s", v.vm.Name(), err, strings.TrimSpace(stderr.String()))
	}
	return nil, &fs.PathError{Op: op, Path: path, Err: err}
}

func (v vmFS) ReadFile(ctx context.Context, path string) ([]byte, error) {
	return v.run(ctx, "open", path, nil, `[ -e "$1" ] || exit 44; exec cat -- "$1"`)
}

// vmWriteScript writes stdin to $1 (through a symlink, like os.WriteFile)
// atomically: a dropped connection leaves the file as it was. A new file gets
// mode $2, an existing one keeps its own.
const vmWriteScript = `mkdir -p -- "$(dirname -- "$1")" || exit 1
f=$(readlink -f -- "$1") || exit 1
t=$f.shelley-tmp.$$
trap 'rm -f -- "$t"' EXIT
if [ -e "$f" ]; then
	cat >"$t" && chmod --reference="$f" -- "$t"
else
	cat >"$t" && chmod "$2" -- "$t"
fi || exit 1
mv -f -- "$t" "$f"`

func (v vmFS) WriteFile(ctx context.Context, path string, data []byte, perm fs.FileMode) error {
	_, err := v.run(ctx, "open", path, data, vmWriteScript, fmt.Sprintf("%o", perm.Perm()))
	return err
}
