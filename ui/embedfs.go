package ui

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// Dist contains the contents of the built UI under dist/.
//
//go:embed dist/*
var Dist embed.FS

var assets http.FileSystem

func init() {
	sub, err := fs.Sub(Dist, "dist")
	if err != nil {
		// If the build is misconfigured and dist/ is missing, fail fast.
		panic(err)
	}
	assets = http.FS(sub)

	// Check if UI sources are stale compared to the embedded build
	checkStaleness()
}

// checkStaleness verifies that the embedded UI build is not stale.
// If ui/src exists and has files modified after the build, we exit with an error.
func checkStaleness() {
	// Read build-info.json from embedded filesystem
	buildInfoData, err := fs.ReadFile(Dist, "dist/build-info.json")
	if err != nil {
		// If build-info.json doesn't exist, the build is old or incomplete.
		fmt.Fprintf(os.Stderr, "\nError: UI build is stale!\n")
		fmt.Fprintf(os.Stderr, "\nPlease run 'make serve' instead of 'go run ./cmd/shelley serve'\n")
		fmt.Fprintf(os.Stderr, "Or rebuild the UI first: cd ui && pnpm run build\n\n")
		os.Exit(1)
		return
	}

	var buildInfo struct {
		Timestamp int64  `json:"timestamp"`
		Date      string `json:"date"`
		SrcDir    string `json:"srcDir"`
	}
	if err := json.Unmarshal(buildInfoData, &buildInfo); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to parse build-info.json: %v\n", err)
		return
	}

	// Allow a small grace window: filesystem mtime granularity (NFS, older
	// extN, build agents with coarser clocks) can land a freshly-built
	// source file a tick after the in-process Date.now() captured by the
	// UI build script, even though nothing actually edited it. Without
	// this slack CI sporadically reports the build as stale.
	const staleSlack = 30 * time.Second
	buildTime := time.UnixMilli(buildInfo.Timestamp).Add(staleSlack)

	// Check if source directory exists (we might be in a deployed binary without source)
	srcDir := buildInfo.SrcDir
	if srcDir == "" {
		// Build info doesn't have srcDir, can't check staleness
		return
	}
	if _, err := os.Stat(srcDir); os.IsNotExist(err) {
		// Source directory doesn't exist, assume we're in production/deployed
		return
	}

	// Walk through ui/src and check if any files are newer than the build
	var newerFiles []string
	err = filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && info.ModTime().After(buildTime) {
			newerFiles = append(newerFiles, path)
		}
		return nil
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to check source file timestamps: %v\n", err)
		return
	}

	if len(newerFiles) > 0 {
		fmt.Fprintf(os.Stderr, "\nError: UI build is stale!\n")
		fmt.Fprintf(os.Stderr, "Build timestamp: %s\n", buildInfo.Date)
		fmt.Fprintf(os.Stderr, "\nThe following source files are newer than the build:\n")
		for _, f := range newerFiles {
			fmt.Fprintf(os.Stderr, "  - %s\n", f)
		}
		fmt.Fprintf(os.Stderr, "\nPlease run 'make serve' instead of 'go run ./cmd/shelley serve'\n")
		fmt.Fprintf(os.Stderr, "Or rebuild the UI first: cd ui && pnpm run build\n\n")
		os.Exit(1)
	}
}

// Assets returns an http.FileSystem backed by the embedded UI assets.
func Assets() http.FileSystem {
	return assets
}

// Checksums returns the content checksums for static assets.
// These are computed during build and used for ETag generation.
func Checksums() map[string]string {
	data, err := fs.ReadFile(Dist, "dist/checksums.json")
	if err != nil {
		return nil
	}
	var checksums map[string]string
	if err := json.Unmarshal(data, &checksums); err != nil {
		return nil
	}
	return checksums
}
