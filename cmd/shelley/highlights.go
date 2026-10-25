package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"shelley.exe.dev/commitnotes"
)

func runHighlights(args []string) {
	if len(args) == 0 {
		highlightsUsage()
		os.Exit(1)
	}
	fs := flag.NewFlagSet("highlights "+args[0], flag.ExitOnError)
	dir := fs.String("C", ".", "Git repository directory")
	fs.Parse(args[1:])
	rest := fs.Args()

	fail := func(err error) {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	usage := func(operands string) {
		fmt.Fprintf(os.Stderr, "Usage: shelley highlights %s [-C dir] %s\n", args[0], operands)
		os.Exit(1)
	}

	switch args[0] {
	case "scaffold":
		if len(rest) != 1 {
			usage("<commit>")
		}
		h, err := commitnotes.HighlightsScaffold(*dir, rest[0])
		if err != nil {
			fail(err)
		}
		out, err := json.MarshalIndent(h, "", "  ")
		if err != nil {
			fail(err)
		}
		fmt.Printf("%s\n", out)

	case "verify", "attach":
		if len(rest) != 2 {
			usage("<commit> <highlights.json>")
		}
		data, err := os.ReadFile(rest[1])
		if err != nil {
			fail(err)
		}
		h, err := commitnotes.ParseHighlights(data)
		if err != nil {
			fail(err)
		}
		// Resolve the commit once: HEAD may move while attaching.
		hash, err := commitnotes.CommitHash(*dir, rest[0])
		if err != nil {
			fail(err)
		}
		if err := commitnotes.VerifyHighlights(*dir, hash, h); err != nil {
			fail(err)
		}
		if args[0] == "verify" {
			fmt.Printf("highlights for %s verify\n", rest[0])
			return
		}
		// Store the parsed form so the note is canonical JSON, with
		// comments' <, > and & left readable.
		var note bytes.Buffer
		enc := json.NewEncoder(&note)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		if err := enc.Encode(h); err != nil {
			fail(err)
		}
		if err := commitnotes.WriteHighlights(*dir, hash, note.Bytes()); err != nil {
			fail(err)
		}
		fmt.Printf("attached highlights note to %s\n", rest[0])

	case "show":
		if len(rest) != 1 {
			usage("<commit>")
		}
		data, err := commitnotes.ReadHighlights(*dir, rest[0])
		if err != nil {
			fail(err)
		}
		if _, err := os.Stdout.Write(data); err != nil {
			fail(err)
		}

	default:
		fmt.Fprintf(os.Stderr, "Unknown highlights subcommand: %s\n", args[0])
		highlightsUsage()
		os.Exit(1)
	}
}

func highlightsUsage() {
	fmt.Fprintln(os.Stderr, "Usage:")
	fmt.Fprintln(os.Stderr, "  shelley highlights scaffold [-C dir] <commit>                  Print a skeleton listing every changed file")
	fmt.Fprintln(os.Stderr, "  shelley highlights verify [-C dir] <commit> <highlights.json>  Check highlights against the commit")
	fmt.Fprintln(os.Stderr, "  shelley highlights attach [-C dir] <commit> <highlights.json>  Verify, then store the highlights as a git note")
	fmt.Fprintln(os.Stderr, "  shelley highlights show [-C dir] <commit>                      Print the stored highlights note")
}
