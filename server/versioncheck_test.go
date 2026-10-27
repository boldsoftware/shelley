package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"testing/synctest"
	"time"
)

func TestDoUpgradeRejectsConcurrentRequests(t *testing.T) {
	for _, stage := range []string{"checksums", "binary"} {
		for _, failDownload := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/fail=%t", stage, failDownload), func(t *testing.T) {
				target := filepath.Join(t.TempDir(), "shelley")
				if err := os.WriteFile(target, []byte("original"), 0o755); err != nil {
					t.Fatal(err)
				}
				oldExe, oldErr := startupExecutable, startupExecutableErr
				startupExecutable, startupExecutableErr = target, nil
				t.Cleanup(func() { startupExecutable, startupExecutableErr = oldExe, oldErr })

				entered, release := make(chan struct{}), make(chan struct{})
				unblock := sync.OnceFunc(func() { close(release) })
				var checksums atomic.Int32
				binary := []byte("updated")
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/checksums":
						if checksums.Add(1) != 1 {
							http.Error(w, "checksum unavailable", http.StatusServiceUnavailable)
							return
						}
						if stage == "checksums" {
							close(entered)
							<-release
						}
						fmt.Fprintf(w, "%x  shelley_%s_%s\n", sha256.Sum256(binary), runtime.GOOS, runtime.GOARCH)
					case "/binary":
						if stage == "binary" {
							close(entered)
							<-release
						}
						if failDownload {
							http.Error(w, "download unavailable", http.StatusServiceUnavailable)
							return
						}
						w.Write(binary)
					default:
						http.NotFound(w, r)
					}
				}))
				defer srv.Close()
				defer unblock()
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				info := &VersionInfo{
					HasUpdate: true, DownloadURL: srv.URL + "/binary",
					ReleaseInfo: &ReleaseInfo{ChecksumsURL: srv.URL + "/checksums"},
				}
				vc := &VersionChecker{lastCheck: time.Now(), cachedInfo: info}
				done := make(chan struct{})
				var upgradeErr error
				go func() {
					upgradeErr = vc.DoUpgrade(ctx)
					close(done)
				}()
				defer func() {
					unblock()
					<-done
				}()
				select {
				case <-entered:
				case <-ctx.Done():
					t.Fatal("first upgrade never reached " + stage)
				}
				if got, err := vc.Check(ctx, false); err != nil || got != info {
					t.Fatalf("version check during upgrade = %v, %v; want cached info", got, err)
				}
				rejected := make(chan error, 1)
				go func() { rejected <- vc.DoUpgrade(ctx) }()
				select {
				case err := <-rejected:
					if err == nil || err.Error() != "upgrade already in progress" {
						t.Errorf("overlapping upgrade = %v; want upgrade already in progress", err)
					}
				case <-ctx.Done():
					t.Fatal("overlapping upgrade did not return promptly")
				}
				if got := checksums.Load(); got != 1 {
					t.Errorf("checksum requests = %d; want 1", got)
				}
				unblock()
				<-done
				wantContents := "updated"
				if failDownload {
					wantContents = "original"
					if upgradeErr == nil || !strings.Contains(upgradeErr.Error(), "download returned status 503") {
						t.Errorf("first upgrade = %v; want download failure", upgradeErr)
					}
				} else if upgradeErr != nil {
					t.Errorf("first upgrade: %v", upgradeErr)
				}
				if got, err := os.ReadFile(target); err != nil || string(got) != wantContents {
					t.Errorf("binary after upgrade = %q, %v; want %q", got, err, wantContents)
				}
				if err := vc.DoUpgrade(ctx); err == nil || !strings.Contains(err.Error(), "failed to fetch checksum") {
					t.Errorf("later upgrade = %v; want checksum failure, not a held lock", err)
				}
			})
		}
	}
}

func TestExtractSHAFromTag(t *testing.T) {
	t.Parallel()
	tests := []struct {
		tag      string
		expected string
	}{
		// Tag format: v0.COUNT.9OCTAL where OCTAL is the SHA in octal
		// For example, 6-char hex SHA "abc123" (hex) = 0xabc123 = 11256099 (decimal)
		// In octal: 52740443
		{"v0.178.952740443", "abc123"}, // SHA abc123 in octal is 52740443
		{"v0.178.933471105", "6e7245"}, // Real release tag
		{"v0.1.90", "000000"},          // SHA 0
		{"", ""},
		{"invalid", ""},
		{"v", ""},
		{"v0", ""},
		{"v0.1", ""},
		{"v0.1.0", ""},  // No '9' prefix
		{"v0.1.8x", ""}, // Invalid octal digit
	}

	for _, tt := range tests {
		t.Run(tt.tag, func(t *testing.T) {
			result := extractSHAFromTag(tt.tag)
			if result != tt.expected {
				t.Errorf("extractSHAFromTag(%q) = %q, want %q", tt.tag, result, tt.expected)
			}
		})
	}
}

func TestParseMinorVersion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		tag      string
		expected int
	}{
		{"v0.1.0", 1},
		{"v0.2.3", 2},
		{"v0.10.5", 10},
		{"v0.100.0", 100},
		{"v1.2.3", 2}, // Should still get minor even with major > 0
		{"", 0},
		{"invalid", 0},
		{"v", 0},
		{"v0", 0},
		{"v0.", 0},
	}

	for _, tt := range tests {
		t.Run(tt.tag, func(t *testing.T) {
			result := parseMinorVersion(tt.tag)
			if result != tt.expected {
				t.Errorf("parseMinorVersion(%q) = %d, want %d", tt.tag, result, tt.expected)
			}
		})
	}
}

func TestIsNewerMinor(t *testing.T) {
	t.Parallel()
	vc := &VersionChecker{}

	tests := []struct {
		name       string
		currentTag string
		latestTag  string
		expected   bool
	}{
		{
			name:       "newer minor version",
			currentTag: "v0.1.0",
			latestTag:  "v0.2.0",
			expected:   true,
		},
		{
			name:       "same version",
			currentTag: "v0.2.0",
			latestTag:  "v0.2.0",
			expected:   false,
		},
		{
			name:       "older version (downgrade)",
			currentTag: "v0.3.0",
			latestTag:  "v0.2.0",
			expected:   false,
		},
		{
			name:       "patch version only",
			currentTag: "v0.2.0",
			latestTag:  "v0.2.5",
			expected:   false, // Minor didn't change
		},
		{
			name:       "multiple minor versions ahead",
			currentTag: "v0.1.0",
			latestTag:  "v0.5.0",
			expected:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := vc.isNewerMinor(tt.currentTag, tt.latestTag)
			if result != tt.expected {
				t.Errorf("isNewerMinor(%q, %q) = %v, want %v",
					tt.currentTag, tt.latestTag, result, tt.expected)
			}
		})
	}
}

func TestVersionCheckerSkipCheck(t *testing.T) {
	t.Setenv("SHELLEY_SKIP_VERSION_CHECK", "true")

	vc := NewVersionChecker()
	if !vc.skipCheck {
		t.Error("Expected skipCheck to be true when SHELLEY_SKIP_VERSION_CHECK=true")
	}

	info, err := vc.Check(t.Context(), false)
	if err != nil {
		t.Errorf("Check() returned error: %v", err)
	}
	if info.HasUpdate {
		t.Error("Expected HasUpdate to be false when skip check is enabled")
	}
}

func TestVersionCheckerCustomized(t *testing.T) {
	t.Setenv("SHELLEY_CUSTOMIZED_OVERRIDE", "true")
	t.Setenv("SHELLEY_SKIP_VERSION_CHECK", "true")
	home := t.TempDir()
	t.Setenv("HOME", home)

	vc := NewVersionChecker()
	info, err := vc.Check(t.Context(), false)
	if err != nil {
		t.Fatalf("Check() returned error: %v", err)
	}
	if !info.Customized {
		t.Error("expected Customized to be true")
	}
	// No checkout on disk: don't advertise a dir the rebase-upgrade
	// conversation can't start in.
	if info.CustomizationDir != "" {
		t.Errorf("expected empty CustomizationDir without a checkout, got %q", info.CustomizationDir)
	}

	dir := CustomizationDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	info, err = vc.Check(t.Context(), false)
	if err != nil {
		t.Fatalf("Check() returned error: %v", err)
	}
	if info.CustomizationDir != dir {
		t.Errorf("expected CustomizationDir %q, got %q", dir, info.CustomizationDir)
	}
}

func TestDoUpgradeRefusesCustomizedBuild(t *testing.T) {
	t.Setenv("SHELLEY_CUSTOMIZED_OVERRIDE", "true")

	vc := &VersionChecker{}
	err := vc.DoUpgrade(t.Context())
	if err == nil {
		t.Fatal("expected DoUpgrade to refuse customized builds")
	}
	if !strings.Contains(err.Error(), "customized") {
		t.Errorf("expected error to mention customized build, got: %v", err)
	}
}

func TestCustomCommits(t *testing.T) {
	t.Parallel()
	if commits := customCommits(""); commits != nil {
		t.Errorf("expected nil for empty dir, got %v", commits)
	}
	if commits := customCommits(t.TempDir()); commits != nil {
		t.Errorf("expected nil for non-repo dir, got %v", commits)
	}

	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-b", "main")
	git("commit", "--allow-empty", "-m", "mainline commit")
	git("update-ref", "refs/remotes/origin/main", "main")
	git("checkout", "-b", "custom")
	git("commit", "--allow-empty", "-m", "custom: first tweak")
	git("commit", "--allow-empty", "-m", "custom: second tweak")

	commits := customCommits(dir)
	if len(commits) != 2 {
		t.Fatalf("expected 2 custom commits, got %d: %v", len(commits), commits)
	}
	if commits[0].Message != "custom: second tweak" || commits[1].Message != "custom: first tweak" {
		t.Errorf("unexpected commit order/messages: %v", commits)
	}
	for _, c := range commits {
		if c.SHA == "" || c.Author != "Test" || c.Date.IsZero() {
			t.Errorf("incomplete commit info: %+v", c)
		}
	}
}

func TestVersionCheckerCache(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// Keep this test serial: the metadata requests use http.DefaultClient.
		oldClient := http.DefaultClient
		t.Cleanup(func() { http.DefaultClient = oldClient })

		requests := 0
		latestTag := "v0.10.0"
		platform := runtime.GOOS + "_" + runtime.GOARCH
		const downloadURL = "https://example.com/shelley"
		http.DefaultClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.Method != http.MethodGet || req.URL.String() != staticMetadataURL+"/release.json" {
				return nil, fmt.Errorf("unexpected metadata request: %s %s", req.Method, req.URL)
			}
			requests++
			body, err := json.Marshal(ReleaseInfo{
				TagName:      latestTag,
				DownloadURLs: map[string]string{platform: downloadURL},
			})
			if err != nil {
				return nil, err
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body))}, nil
		})}

		vc := &VersionChecker{}
		check := func(force bool, wantTag string, wantRequests int) {
			t.Helper()
			info, err := vc.Check(t.Context(), force)
			if err != nil {
				t.Fatal(err)
			}
			if info.Error != "" {
				t.Fatalf("Check returned error: %s", info.Error)
			}
			if info.LatestTag != wantTag || info.DownloadURL != downloadURL {
				t.Fatalf("Check = tag %q, download %q; want %q, %q", info.LatestTag, info.DownloadURL, wantTag, downloadURL)
			}
			if requests != wantRequests {
				t.Fatalf("metadata requests = %d, want %d", requests, wantRequests)
			}
		}

		check(false, "v0.10.0", 1)
		latestTag = "v0.11.0"
		check(false, "v0.10.0", 1) // A fresh cache must not fetch the newer release.
		check(true, "v0.11.0", 2)  // A forced check must fetch and replace the cache.
		latestTag = "v0.12.0"
		check(false, "v0.11.0", 2)
		time.Sleep(6 * time.Hour)  // synctest advances fake time, not wall time.
		check(false, "v0.12.0", 3) // An expired cache must fetch without forcing.
	})
}

func TestFindHeadlessShell(t *testing.T) {
	t.Parallel()
	platform := runtime.GOOS + "_" + runtime.GOARCH
	// The shape scripts/generate-version-metadata.py writes.
	metadata := fmt.Sprintf(`{
		"tag_name": "v0.10.0",
		"headless_shells": {
			%q: {"version": "Chromium 156.0.8078.48.p1", "url": "https://example.com/headless-shell.tar.gz"},
			"plan9_mips": {"version": "Chromium 157.0.8103.6", "url": "https://example.com/other.tar.gz"}
		},
		"headless_shell_version": "Chromium 1.0.0.0",
		"headless_shell_urls": {%[1]q: "https://example.com/legacy.tar.gz"}
	}`, platform)
	var release ReleaseInfo
	if err := json.Unmarshal([]byte(metadata), &release); err != nil {
		t.Fatal(err)
	}
	want := HeadlessShellBuild{Version: "Chromium 156.0.8078.48.p1", URL: "https://example.com/headless-shell.tar.gz"}
	if got, ok := findHeadlessShell(&release); !ok || got != want {
		t.Errorf("findHeadlessShell() = %+v, %v; want %+v, true", got, ok, want)
	}
	delete(release.HeadlessShells, platform)
	if got, ok := findHeadlessShell(&release); ok {
		t.Errorf("findHeadlessShell() without a build for %s = %+v, want none", platform, got)
	}
}

func TestFindDownloadURL(t *testing.T) {
	t.Parallel()
	platform := runtime.GOOS + "_" + runtime.GOARCH
	tests := []struct {
		name string
		urls map[string]string
		want string
	}{
		{
			name: "current platform",
			urls: map[string]string{
				platform:                  "https://example.com/current",
				runtime.GOOS + "_other":   "https://example.com/other-arch",
				"other_" + runtime.GOARCH: "https://example.com/other-os",
			},
			want: "https://example.com/current",
		},
		{name: "different architecture", urls: map[string]string{runtime.GOOS + "_other": "https://example.com/other-arch"}},
		{name: "different OS", urls: map[string]string{"other_" + runtime.GOARCH: "https://example.com/other-os"}},
		{name: "no downloads"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vc := &VersionChecker{}
			got := vc.findDownloadURL(&ReleaseInfo{DownloadURLs: tt.urls})
			if got != tt.want {
				t.Errorf("findDownloadURL = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFetchChangelogPrefixMatching(t *testing.T) {
	// The February 12 regression: tags encode six hex digits, but the
	// published commits use seven. Only the latest commit is after current.
	got := fetchChangelogForTest(t, []StaticCommitInfo{
		{SHA: "bbbbbbb", Subject: "newer than latest"},
		{SHA: "a004332", Subject: "fix: latest commit"},
		{SHA: "542901e", Subject: "current commit"},
		{SHA: "e3ed88a", Subject: "older commit"},
	})
	want := []CommitInfo{{SHA: "a004332", Message: "fix: latest commit"}}
	if !slices.Equal(got, want) {
		t.Fatalf("FetchChangelog = %+v, want %+v", got, want)
	}
}

func TestFetchChangelogPrefixMatchingMultipleCommits(t *testing.T) {
	got := fetchChangelogForTest(t, []StaticCommitInfo{
		{SHA: "bbbbbbb", Subject: "newer than latest"},
		{SHA: "a004332", Subject: "fix: latest commit"},
		{SHA: "1111111", Subject: "middle commit 1"},
		{SHA: "2222222", Subject: "middle commit 2"},
		{SHA: "542901e", Subject: "current commit"},
		{SHA: "60ee3ab", Subject: "old commit"},
	})
	want := []CommitInfo{
		{SHA: "a004332", Message: "fix: latest commit"},
		{SHA: "1111111", Message: "middle commit 1"},
		{SHA: "2222222", Message: "middle commit 2"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("FetchChangelog = %+v, want %+v", got, want)
	}
}

func fetchChangelogForTest(t *testing.T, commits []StaticCommitInfo) []CommitInfo {
	t.Helper()
	// Callers must stay serial because FetchChangelog uses http.DefaultClient.
	oldClient := http.DefaultClient
	t.Cleanup(func() { http.DefaultClient = oldClient })
	body, err := json.Marshal(commits)
	if err != nil {
		t.Fatal(err)
	}
	const currentTag = "v0.212.925024401" // 542901, in octal
	const latestTag = "v0.213.950002063"  // a00433, in octal
	requests := 0
	http.DefaultClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet || req.URL.String() != staticMetadataURL+"/commits.json?v="+latestTag {
			return nil, fmt.Errorf("unexpected changelog request: %s %s", req.Method, req.URL)
		}
		requests++
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body))}, nil
	})}
	vc := &VersionChecker{}
	got, err := vc.FetchChangelog(t.Context(), currentTag, latestTag)
	if err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("changelog requests = %d, want 1", requests)
	}
	return got
}

func TestHeadlessShellHasUpdate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		current  string
		latest   string
		expected bool
	}{
		{"newer major", "Chromium 141.0.7390.55", "Chromium 147.0.7727.24", true},
		{"same version", "Chromium 141.0.7390.55", "Chromium 141.0.7390.55", false},
		{"older version", "Chromium 147.0.7727.24", "Chromium 141.0.7390.55", false},
		{"newer patch", "Chromium 141.0.7390.55", "Chromium 141.0.7390.56", true},
		{"numeric not lexicographic", "Chromium 999.0.0.0", "Chromium 1001.0.0.0", true},
		{"numeric not lexicographic reverse", "Chromium 1001.0.0.0", "Chromium 999.0.0.0", false},
		{"patch level over the same version", "Chromium 156.0.8078.48", "Chromium 156.0.8078.48.p1", true},
		{"patch level over an older version", "Chromium 156.0.8078.36", "Chromium 156.0.8078.48.p1", true},
		{"same patch level", "Chromium 156.0.8078.48.p1", "Chromium 156.0.8078.48.p1", false},
		{"same version without the patch level", "Chromium 156.0.8078.48.p1", "Chromium 156.0.8078.48", false},
		{"newer patch level", "Chromium 156.0.8078.48.p1", "Chromium 156.0.8078.48.p2", true},
		{"patch level numeric not lexicographic", "Chromium 156.0.8078.48.p2", "Chromium 156.0.8078.48.p10", true},
		{"newer version without a patch level", "Chromium 156.0.8078.48.p1", "Chromium 156.0.8078.60", true},
		{"malformed patch level", "Chromium 156.0.8078.48", "Chromium 156.0.8078.48.x1", false},
		{"three parts", "Chromium 156.0.8078", "Chromium 156.0.8078.48", false},
		{"five numeric parts", "Chromium 156.0.8078.48", "Chromium 156.0.8078.48.1", false},
		{"empty patch level", "Chromium 156.0.8078.48", "Chromium 156.0.8078.48.p", false},
		{"uppercase patch level", "Chromium 156.0.8078.48", "Chromium 156.0.8078.48.P1", false},
		{"empty current", "", "Chromium 141.0.7390.55", false},
		{"empty latest", "Chromium 141.0.7390.55", "", false},
		{"both empty", "", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := headlessShellHasUpdate(tt.current, tt.latest)
			if result != tt.expected {
				t.Errorf("headlessShellHasUpdate(%q, %q) = %v, want %v",
					tt.current, tt.latest, result, tt.expected)
			}
		})
	}
}

func TestLocalHeadlessShellVersion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const version = "Chromium 156.0.8078.48"
	const release = "Chromium 156.0.8078.48.p1"
	tests := []struct {
		name   string
		record func(t *testing.T, bin string)
		want   string
	}{
		{"no recorded version", func(t *testing.T, bin string) {}, version},
		{"recorded", func(t *testing.T, bin string) {
			if err := recordHeadlessShellVersion(bin, release); err != nil {
				t.Fatal(err)
			}
		}, release},
		{"recorded for another binary of the same version", func(t *testing.T, bin string) {
			if err := recordHeadlessShellVersion(bin, release); err != nil {
				t.Fatal(err)
			}
			writeShellScript(t, bin, "# unpatched\necho '"+version+"'")
		}, version},
		{"garbage", func(t *testing.T, bin string) {
			if err := os.WriteFile(filepath.Join(filepath.Dir(bin), headlessShellVersionFile), []byte(release+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}, version},
		{"binary fails", func(t *testing.T, bin string) {
			if err := recordHeadlessShellVersion(bin, release); err != nil {
				t.Fatal(err)
			}
			writeShellScript(t, bin, "exit 1")
		}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			bin := filepath.Join(t.TempDir(), "headless-shell")
			writeShellScript(t, bin, "echo '"+version+"'")
			tt.record(t, bin)
			got := getLocalHeadlessShellVersion(ctx, bin)
			if got != tt.want {
				t.Errorf("getLocalHeadlessShellVersion() = %q, want %q", got, tt.want)
			}
			if tt.want == release && headlessShellHasUpdate(got, release) {
				t.Errorf("an update to the recorded %s is still offered", release)
			}
		})
	}
	if got := getLocalHeadlessShellVersion(ctx, filepath.Join(t.TempDir(), "headless-shell")); got != "" {
		t.Errorf("getLocalHeadlessShellVersion(missing) = %q, want \"\"", got)
	}
}

func TestIsHeadlessShellRelease(t *testing.T) {
	t.Parallel()
	tests := []struct {
		release, version string
		want             bool
	}{
		{"Chromium 156.0.8078.48", "Chromium 156.0.8078.48", true},
		{"Chromium 156.0.8078.48.p1", "Chromium 156.0.8078.48", true},
		{"Chromium 156.0.8078.60.p1", "Chromium 156.0.8078.48", false},
		{"Chromium 156.0.8078.4", "Chromium 156.0.8078.48", false},
		{"Chromium 156.0.8078.48.p1", "HeadlessChrome", false},
	}
	for _, tt := range tests {
		if got := isHeadlessShellRelease(tt.release, tt.version); got != tt.want {
			t.Errorf("isHeadlessShellRelease(%q, %q) = %v, want %v", tt.release, tt.version, got, tt.want)
		}
	}
}

// writeShellScript writes an executable shell script with body to bin.
func writeShellScript(t *testing.T, bin, body string) {
	t.Helper()
	// A process forked while bin is open for writing keeps it open until it
	// execs, and running bin meanwhile fails with ETXTBSY
	// (https://go.dev/issue/22315). ForkLock keeps forks out until it's closed.
	syscall.ForkLock.RLock()
	defer syscall.ForkLock.RUnlock()
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestIsPermissionError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "fs.ErrPermission",
			err:      fs.ErrPermission,
			expected: true,
		},
		{
			name:     "os.ErrPermission",
			err:      os.ErrPermission,
			expected: true,
		},
		{
			name:     "wrapped fs.ErrPermission",
			err:      errors.Join(errors.New("outer"), fs.ErrPermission),
			expected: true,
		},
		{
			name:     "other error",
			err:      errors.New("some other error"),
			expected: false,
		},
		{
			name:     "nil error",
			err:      nil,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isPermissionError(tt.err)
			if result != tt.expected {
				t.Errorf("isPermissionError(%v) = %v, want %v", tt.err, result, tt.expected)
			}
		})
	}
}

// TestDoUpgradeFallsBackToSudo upgrades a binary in a root-owned directory,
// like /usr/local/bin, and checks that the sudo fallback keeps its owner and
// mode using only commands and flags macOS has too.
func TestDoUpgradeFallsBackToSudo(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write the directory, so nothing falls back to sudo")
	}
	if !isSudoAvailable() {
		t.Skip("needs passwordless sudo")
	}
	dir := filepath.Join(t.TempDir(), "bin")
	exe := filepath.Join(dir, "shelley")
	old := filepath.Join(t.TempDir(), "old")
	if err := os.WriteFile(old, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { exec.Command("sudo", "rm", "-rf", dir).Run() })
	for _, args := range [][]string{{"mkdir", dir}, {"cp", old, exe}, {"chown", "1:1", exe}, {"chmod", "745", exe}} {
		if out, err := exec.Command("sudo", args...).CombinedOutput(); err != nil {
			t.Fatalf("sudo %v: %v: %s", args, err, out)
		}
	}

	newBinary := []byte("new")
	asset := fmt.Sprintf("shelley_%s_%s", runtime.GOOS, runtime.GOARCH)
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("GET /release.json", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(ReleaseInfo{
			TagName:      "v0.2.0",
			DownloadURLs: map[string]string{runtime.GOOS + "_" + runtime.GOARCH: srv.URL + "/" + asset},
			ChecksumsURL: srv.URL + "/checksums.txt",
		})
	})
	mux.HandleFunc("GET /checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%x  %s\n", sha256.Sum256(newBinary), asset)
	})
	mux.HandleFunc("GET /"+asset, func(w http.ResponseWriter, r *http.Request) { w.Write(newBinary) })

	t.Setenv("SHELLEY_VERSION_OVERRIDE", "v0.1.0")
	oldURL, oldExe, oldErr := staticMetadataURL, startupExecutable, startupExecutableErr
	staticMetadataURL, startupExecutable, startupExecutableErr = srv.URL, exe, nil
	t.Cleanup(func() { staticMetadataURL, startupExecutable, startupExecutableErr = oldURL, oldExe, oldErr })

	if err := (&VersionChecker{}).DoUpgrade(t.Context()); err != nil {
		t.Fatal(err)
	}

	if got, err := os.ReadFile(exe); err != nil || string(got) != "new" {
		t.Fatalf("contents = %q, %v; want %q", got, err, "new")
	}
	fi, err := os.Stat(exe)
	if err != nil {
		t.Fatal(err)
	}
	st := fi.Sys().(*syscall.Stat_t)
	if st.Uid != 1 || st.Gid != 1 || fi.Mode().Perm() != 0o745 {
		t.Errorf("owner %d:%d mode %o; want 1:1 745", st.Uid, st.Gid, fi.Mode().Perm())
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("left behind %v; want only shelley", entries)
	}
}
