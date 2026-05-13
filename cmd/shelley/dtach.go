package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"shelley.exe.dev/dtach"
)

// ptyEnv returns the parent environment with TERM/COLORTERM forced to values
// suitable for the xterm.js-backed web terminal (and any real TTY attacher).
// Without this, `shelley dtach new` inherits whatever TERM shelley itself was
// started with — often "dumb" or unset — which makes less, git pagers, vim,
// etc. complain that the terminal is not fully functional.
func ptyEnv() []string {
	env := os.Environ()
	out := env[:0]
	for _, kv := range env {
		if strings.HasPrefix(kv, "TERM=") || strings.HasPrefix(kv, "COLORTERM=") {
			continue
		}
		out = append(out, kv)
	}
	out = append(out, "TERM=xterm-256color", "COLORTERM=truecolor")
	return out
}

func runDtach(args []string) {
	if len(args) == 0 {
		dtachUsage()
		os.Exit(1)
	}
	switch args[0] {
	case "new":
		runDtachNew(args[1:])
	case "attach":
		runDtachAttach(args[1:])
	default:
		dtachUsage()
		os.Exit(1)
	}
}

func dtachUsage() {
	fmt.Fprintf(os.Stderr, "Usage:\n")
	fmt.Fprintf(os.Stderr, "  shelley dtach new -s SOCKET [-cwd DIR] [-cols N -rows N] -- CMD [ARGS...]\n")
	fmt.Fprintf(os.Stderr, "  shelley dtach attach -s SOCKET\n")
	fmt.Fprintf(os.Stderr, "\nThere is no detach key: close the terminal (or kill the attach\nprocess) to detach. The session keeps running until its command exits.\n")
}

func runDtachNew(args []string) {
	fs := flag.NewFlagSet("dtach new", flag.ExitOnError)
	socket := fs.String("s", "", "Unix socket path (required)")
	cwd := fs.String("cwd", "", "working directory")
	cols := fs.Int("cols", 80, "initial cols")
	rows := fs.Int("rows", 24, "initial rows")
	fs.Parse(args)

	if *socket == "" || fs.NArg() == 0 {
		dtachUsage()
		os.Exit(2)
	}
	rest := fs.Args()
	if err := dtach.Serve(dtach.ServerOptions{
		SocketPath: *socket,
		Command:    rest[0],
		Args:       rest[1:],
		Dir:        *cwd,
		Cols:       uint16(*cols),
		Rows:       uint16(*rows),
		Env:        ptyEnv(),
	}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runDtachAttach(args []string) {
	fs := flag.NewFlagSet("dtach attach", flag.ExitOnError)
	socket := fs.String("s", "", "Unix socket path (required)")
	fs.Parse(args)
	if *socket == "" {
		dtachUsage()
		os.Exit(2)
	}
	code, err := dtach.AttachTTY(*socket)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(code)
}
