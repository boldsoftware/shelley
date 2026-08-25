package main

import (
	"fmt"
	"os"

	"shelley.exe.dev/exescroll"
)

func runExeScroll(args []string) {
	if err := exescroll.Exec(args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
