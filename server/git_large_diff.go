package server

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const gitDiffMaxBytes = 2 << 20

type gitDiffSide struct {
	exists bool
	size   int64
	object string
	path   string
}

func (m machine) gitDiffBlob(root, ref, path string) (gitDiffSide, error) {
	out, err := m.git(root, "ls-tree", "-l", "-z", ref, "--", ":(literal)"+path).Output()
	if err != nil {
		return gitDiffSide{}, withStderr(err)
	}
	if len(out) == 0 {
		return gitDiffSide{}, nil
	}
	metadata, _, ok := strings.Cut(string(out), "\t")
	fields := strings.Fields(metadata)
	if !ok || len(fields) != 4 {
		return gitDiffSide{}, fmt.Errorf("invalid tree entry for %s", path)
	}
	if fields[1] != "blob" {
		return gitDiffSide{}, nil
	}
	size, err := strconv.ParseInt(fields[3], 10, 64)
	if err != nil {
		return gitDiffSide{}, err
	}
	return gitDiffSide{exists: true, size: size, object: fields[2]}, nil
}

func (m machine) gitDiffWorkingFile(root, path string) (gitDiffSide, error) {
	fullPath := m.join(root, path)
	isDir, size, err := m.stat(fullPath)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return gitDiffSide{}, nil
	}
	if err != nil {
		return gitDiffSide{}, err
	}
	if isDir {
		return gitDiffSide{}, nil
	}
	return gitDiffSide{exists: true, size: size, path: fullPath}, nil
}

// A negative limit is for explicit downloads; preview reads are always bounded.
func (m machine) readGitDiffSide(root string, side gitDiffSide, limit int64) ([]byte, error) {
	if !side.exists {
		return nil, nil
	}
	if side.path != "" && m.vm == nil {
		f, err := os.Open(side.path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		if limit < 0 {
			return io.ReadAll(f)
		}
		return io.ReadAll(io.LimitReader(f, limit))
	}
	var cmd *exec.Cmd
	if side.object != "" {
		cmd = m.git(root, "cat-file", "blob", side.object)
	} else {
		argv := m.vm.Command(`exec cat -- "$1"`, side.path)
		cmd = exec.CommandContext(m.ctx, argv[0], argv[1:]...)
	}
	if limit < 0 {
		return cmd.Output()
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	out, readErr := io.ReadAll(io.LimitReader(stdout, limit))
	truncated := int64(len(out)) == limit
	if truncated || readErr != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if readErr != nil {
		return nil, readErr
	}
	if waitErr != nil && !truncated {
		return nil, fmt.Errorf("read file: %w: %s", waitErr, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func gitDiffLimitReason(old, new []byte) string {
	if len(old)+len(new) > gitDiffMaxBytes {
		return "Combined file size exceeds 2 MiB."
	}
	for _, data := range [][]byte{old, new} {
		lines, lineBytes := 1, 0
		for _, b := range data {
			if b == '\n' {
				lines++
				lineBytes = 0
			} else {
				lineBytes++
			}
			if lines > 20000 {
				return "A file exceeds 20,000 lines."
			}
			if lineBytes > 20000 {
				return "A line exceeds 20,000 bytes."
			}
		}
	}
	return ""
}

func (m machine) serveGitPatch(w http.ResponseWriter, r *http.Request, root string, revs []string, path, oldPath string, oldSide, newSide gitDiffSide) {
	options := []string{"diff", "--no-ext-diff", "--no-textconv", "--no-color", "--no-relative", "--src-prefix=a/", "--dst-prefix=b/", "--binary"}
	args := append([]string{}, options...)
	args = append(args, revs...)
	args = append(args, "--", ":(literal)"+path, ":(exclude,literal)"+path+"/")
	if oldPath != path {
		args = append(args, ":(literal)"+oldPath, ":(exclude,literal)"+oldPath+"/")
	}
	noIndex := false
	if len(revs) == 1 && !oldSide.exists && newSide.exists {
		tracked, err := m.git(root, "ls-files", "-z", "--", ":(literal)"+path, ":(exclude,literal)"+path+"/").Output()
		if err != nil {
			http.Error(w, withStderr(err).Error(), http.StatusInternalServerError)
			return
		}
		if len(tracked) == 0 {
			noIndex = true
			args = append([]string{}, options...)
			workingPath := path
			if workingPath == "-" {
				workingPath = "./-"
				// Let git apply strip ./ rather than leave it in the filename.
				args = append(args, "--no-prefix")
			}
			args = append(args, "--no-index", "--", os.DevNull, workingPath)
		}
	}
	// Spool before sending headers so a failed git command is an HTTP error,
	// not a successful download containing a truncated patch.
	f, err := os.CreateTemp("", "shelley-diff-*.patch")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer os.Remove(f.Name())
	defer f.Close()
	cmd := m.git(root, args...)
	cmd.Stdout = f
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err = cmd.Run()
	var exitErr *exec.ExitError
	if noIndex && errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		err = nil // --no-index uses 1 for a successful diff with changes.
	}
	if err != nil {
		http.Error(w, fmt.Sprintf("generate patch: %v: %s", err, strings.TrimSpace(stderr.String())), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/x-patch")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(path) + ".patch"}))
	http.ServeContent(w, r, filepath.Base(path)+".patch", time.Time{}, f)
}
