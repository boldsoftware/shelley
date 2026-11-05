package server

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/commitnotes"
	"shelley.exe.dev/db"
)

// A machine is where a conversation's files are: this host, or the VM its
// tools run on. The UI's views of a conversation's work (the diff viewer, git
// graph, file editor and terminal) go through one, so they show the VM's
// repository rather than this host's.
type machine struct {
	ctx context.Context
	// vm is nil for this host.
	vm      *conversationVMTarget
	scratch string // the conversation's working directory here
	files   claudetool.HostFS
}

var localMachine = machine{ctx: context.Background(), files: claudetool.NewHostFS(nil)}

// requestMachine is the machine of r's conversation_id parameter: its VM, or
// this host for no conversation or one without a VM. Viewing never provisions
// a VM; it reconnects a ready one, as the next tool call would.
func (s *Server) requestMachine(r *http.Request) (machine, error) {
	return s.conversationMachine(r.Context(), r.URL.Query().Get("conversation_id"))
}

// conversationMachine is the machine of conversation id: its VM, or this
// host for no conversation or one without a VM.
func (s *Server) conversationMachine(ctx context.Context, id string) (machine, error) {
	if id == "" {
		return localMachine, nil
	}
	vm, err := s.db.GetConversationVM(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return localMachine, nil
	}
	if err != nil {
		return machine{}, fmt.Errorf("load conversation VM: %w", err)
	}
	if err := conversationVMArchived(vm); err != nil {
		return machine{}, err
	}
	if vm.Status != db.ConversationVMReady {
		return machine{}, fmt.Errorf("%w: VM %s is not ready (%s)", db.ErrConflict, vm.Name, vm.Status)
	}
	if !s.conversationVMMasterRunning(ctx, *vm) {
		if vm, err = s.prepareConversationVMNow(ctx, vm.ConversationID); err != nil {
			return machine{}, err
		}
	}
	target, err := s.conversationVMTarget(vm)
	if err != nil {
		return machine{}, err
	}
	scratch, err := s.vmScratchDir(vm.ConversationID)
	if err != nil {
		return machine{}, err
	}
	return machine{ctx: ctx, vm: &target, scratch: scratch, files: claudetool.NewHostFS(target)}, nil
}

// machineError reports a requestMachine failure.
func machineError(w http.ResponseWriter, err error) {
	status := http.StatusBadGateway
	if errors.Is(err, db.ErrConflict) {
		status = http.StatusConflict
	}
	http.Error(w, err.Error(), status)
}

// dir is the directory the UI means by cwd: on a VM, the conversation's
// working directory here (or none) stands for the VM's.
func (m machine) dir(cwd string) string {
	if m.vm != nil && (cwd == "" || cwd == m.scratch) {
		return m.vm.Dir()
	}
	return cwd
}

// clean cleans p in the machine's path syntax.
func (m machine) clean(p string) string {
	if m.vm != nil {
		return path.Clean(p)
	}
	return filepath.Clean(p)
}

func (m machine) join(elem ...string) string {
	if m.vm != nil {
		return path.Join(elem...)
	}
	return filepath.Join(elem...)
}

// vmDirScript runs its arguments after the first in that directory.
const vmDirScript = `cd -- "$1" || exit 1
shift
exec "$@"`

// git returns a command running git in dir.
func (m machine) git(dir string, args ...string) *exec.Cmd {
	if m.vm == nil {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		return cmd
	}
	argv := m.vm.Command(vmDirScript, append([]string{dir, "git"}, args...)...)
	return exec.CommandContext(m.ctx, argv[0], argv[1:]...)
}

// repo is the git repository at dir, for commitnotes.
func (m machine) repo(dir string) commitnotes.Repo {
	if m.vm == nil {
		return commitnotes.Local(dir)
	}
	return func(args ...string) *exec.Cmd {
		argv := m.vm.Command(vmDirScript, append([]string{dir, "env", "LC_ALL=C", "git"}, args...)...)
		return exec.CommandContext(m.ctx, argv[0], argv[1:]...)
	}
}

// isDir reports whether p is a directory.
func (m machine) isDir(p string) bool {
	if m.vm == nil {
		info, err := os.Stat(p)
		return err == nil && info.IsDir()
	}
	isDir, _, err := m.stat(p)
	return err == nil && isDir
}

// vmStatScript prints whether $1 is a directory (d) or a regular file (f),
// and its size, or exits 44 if it is missing.
const vmStatScript = `if [ -d "$1" ]; then echo d 0; exit; fi
[ -f "$1" ] || exit 44
echo f $(wc -c <"$1")`

// stat reports whether p is a directory, and its size.
func (m machine) stat(p string) (isDir bool, size int64, err error) {
	if m.vm == nil {
		info, err := os.Stat(p)
		if err != nil {
			return false, 0, err
		}
		return info.IsDir(), info.Size(), nil
	}
	argv := m.vm.Command(vmStatScript, p)
	out, err := exec.CommandContext(m.ctx, argv[0], argv[1:]...).Output()
	if exitErr := (*exec.ExitError)(nil); errors.As(err, &exitErr) && exitErr.ExitCode() == 44 {
		return false, 0, fs.ErrNotExist
	}
	if err != nil {
		return false, 0, fmt.Errorf("stat %s on %s: %w", p, m.vm.Name(), withStderr(err))
	}
	var kind string
	if _, err := fmt.Sscan(strings.TrimSpace(string(out)), &kind, &size); err != nil {
		return false, 0, fmt.Errorf("stat %s on %s: %q", p, m.vm.Name(), out)
	}
	return kind == "d", size, nil
}

func (m machine) readFile(p string) ([]byte, error) { return m.files.ReadFile(m.ctx, p) }

func (m machine) writeFile(p string, data []byte) error {
	return m.files.WriteFile(m.ctx, p, data, 0o644)
}

// gitRoot returns the root of the git repository containing dir.
func (m machine) gitRoot(dir string) (string, error) {
	output, err := m.git(dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

// vmListDirScript lists directory $1 (or $HOME) for the directory picker,
// exiting 44 if it is missing, 45 if it is not a directory, or 46 if it can't
// be read. It prints NUL-terminated fields: the path; the root of the git
// worktree containing it, its HEAD subject, and the main repository's root
// if that worktree is a linked one (all empty outside git); then each
// subdirectory (not symlink) and its HEAD subject if it is a repository.
const vmListDirScript = `p=${1:-$HOME}
[ -e "$p" ] || exit 44
[ -d "$p" ] || exit 45
cd -- "$p" 2>/dev/null && [ -r . ] || exit 46
root=$(git rev-parse --show-toplevel 2>/dev/null)
subject= main=
if [ -n "$root" ]; then
	subject=$(git -C "$root" log -1 --format=%s 2>/dev/null)
	dirs=$(git -C "$root" rev-parse --path-format=absolute --git-dir --git-common-dir 2>/dev/null)
	gitdir=${dirs%%
*} common=${dirs#*
}
	[ "$gitdir" = "$common" ] || main=$(dirname -- "$common")
fi
printf '%s\0%s\0%s\0%s\0' "$p" "$root" "$subject" "$main"
for e in * .*; do
	case $e in .|..) continue;; esac
	[ -d "$e" ] && [ ! -L "$e" ] || continue
	s=
	[ -e "$e/.git" ] && s=$(git -C "$e" log -1 --format=%s 2>/dev/null)
	printf '%s\0%s\0' "$e" "$s"
done`

// listDirectory lists directory p on the machine's VM for the directory
// picker. A problem the user can fix is in the response's Error.
func (m machine) listDirectory(p string) (ListDirectoryResponse, error) {
	if p != "" {
		p = path.Clean(p)
	}
	argv := m.vm.Command(vmListDirScript, p)
	out, err := exec.CommandContext(m.ctx, argv[0], argv[1:]...).Output()
	if exitErr := (*exec.ExitError)(nil); errors.As(err, &exitErr) {
		switch exitErr.ExitCode() {
		case 44:
			return ListDirectoryResponse{Error: "directory does not exist"}, nil
		case 45:
			return ListDirectoryResponse{Error: "path is not a directory"}, nil
		case 46:
			return ListDirectoryResponse{Error: "permission denied"}, nil
		}
	}
	if err != nil {
		return ListDirectoryResponse{}, fmt.Errorf("list %s on %s: %w", p, m.vm.Name(), withStderr(err))
	}
	fields := strings.Split(string(out), "\x00")
	if len(fields) < 5 || len(fields)%2 != 1 {
		return ListDirectoryResponse{}, fmt.Errorf("list %s on %s: %q", p, m.vm.Name(), out)
	}
	response := ListDirectoryResponse{
		Path:            fields[0],
		Parent:          path.Dir(fields[0]),
		GitRepoRoot:     fields[1],
		GitHeadSubject:  fields[2],
		GitWorktreeRoot: fields[3],
	}
	if response.Parent == response.Path {
		response.Parent = ""
	}
	for i := 4; i+1 < len(fields); i += 2 {
		response.Entries = append(response.Entries, DirectoryEntry{Name: fields[i], IsDir: true, GitHeadSubject: fields[i+1]})
	}
	sortDirectoryEntries(response.Entries)
	return response, nil
}

// vmMkdirScript makes directory $1, exiting 44 if its parent is missing and
// 47 if it exists.
const vmMkdirScript = `[ -e "$1" ] && exit 47
[ -d "$(dirname -- "$1")" ] || exit 44
mkdir -- "$1"`

// mkdir makes directory p on the machine's VM for the directory picker. A
// problem the user can fix is the returned message.
func (m machine) mkdir(p string) (problem string, err error) {
	argv := m.vm.Command(vmMkdirScript, path.Clean(p))
	var stderr strings.Builder
	cmd := exec.CommandContext(m.ctx, argv[0], argv[1:]...)
	cmd.Stderr = &stderr
	err = cmd.Run()
	if exitErr := (*exec.ExitError)(nil); errors.As(err, &exitErr) {
		switch exitErr.ExitCode() {
		case 44:
			return "parent directory does not exist", nil
		case 47:
			return "path already exists", nil
		}
	}
	if err != nil {
		return "", fmt.Errorf("mkdir %s on %s: %w: %s", p, m.vm.Name(), err, strings.TrimSpace(stderr.String()))
	}
	return "", nil
}

// withStderr adds what a failed command, run with Output, wrote to stderr,
// such as ssh's reason for exiting 255.
func withStderr(err error) error {
	if exitErr := (*exec.ExitError)(nil); errors.As(err, &exitErr) && len(bytes.TrimSpace(exitErr.Stderr)) > 0 {
		return fmt.Errorf("%w: %s", err, bytes.TrimSpace(exitErr.Stderr))
	}
	return err
}
