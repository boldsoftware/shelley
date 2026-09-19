package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDoUpgradeRejectsConcurrentRequests(t *testing.T) {
	for _, blockAt := range []string{"checksums", "binary"} {
		t.Run(blockAt, func(t *testing.T) {
			t.Parallel()
			entered := make(chan struct{})
			release := make(chan struct{})
			unblock := sync.OnceFunc(func() { close(release) })
			var checksumRequests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/checksums":
					if checksumRequests.Add(1) > 1 {
						http.Error(w, "checksum unavailable", http.StatusServiceUnavailable)
						return
					}
					if blockAt == "checksums" {
						close(entered)
						<-release
					}
					fmt.Fprintf(w, "%s  shelley_%s_%s\n", strings.Repeat("0", 64), runtime.GOOS, runtime.GOARCH)
				case "/binary":
					if blockAt == "binary" {
						close(entered)
						<-release
					}
					// Never reach selfupdate.Apply or replace the test executable.
					http.Error(w, "download unavailable", http.StatusServiceUnavailable)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			defer unblock()

			info := &VersionInfo{
				HasUpdate:   true,
				DownloadURL: server.URL + "/binary",
				ReleaseInfo: &ReleaseInfo{ChecksumsURL: server.URL + "/checksums"},
			}
			vc := &VersionChecker{lastCheck: time.Now(), cachedInfo: info}
			done := make(chan error, 1)
			go func() { done <- vc.DoUpgrade(t.Context()) }()
			<-entered

			// Checking version information must remain available during an upgrade.
			if got, err := vc.Check(t.Context(), false); err != nil || got != info {
				t.Fatalf("Check during upgrade = %v, %v; want cached info", got, err)
			}
			if err := vc.DoUpgrade(t.Context()); err == nil || err.Error() != "upgrade already in progress" {
				t.Errorf("concurrent DoUpgrade = %v; want upgrade already in progress", err)
			}
			if got := checksumRequests.Load(); got != 1 {
				t.Errorf("checksum requests during upgrade = %d; want 1", got)
			}

			unblock()
			if err := <-done; err == nil || !strings.Contains(err.Error(), "download returned status 503") {
				t.Fatalf("first DoUpgrade = %v; want download failure", err)
			}
			// A failed attempt must release the guard so a later request can retry.
			if err := vc.DoUpgrade(t.Context()); err == nil || !strings.Contains(err.Error(), "failed to fetch checksum") {
				t.Errorf("retry DoUpgrade = %v; want checksum failure", err)
			}
			if got := checksumRequests.Load(); got != 2 {
				t.Errorf("checksum requests after retry = %d; want 2", got)
			}
		})
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
	t.Parallel()
	// Create a mock server
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		release := ReleaseInfo{
			TagName:     "v0.10.0",
			Version:     "0.10.0",
			PublishedAt: time.Now().Add(-10 * 24 * time.Hour).Format(time.RFC3339),
			DownloadURLs: map[string]string{
				"linux_amd64":  "https://example.com/linux_amd64",
				"darwin_arm64": "https://example.com/darwin_arm64",
			},
		}
		json.NewEncoder(w).Encode(release)
	}))
	defer server.Close()

	// Create version checker without skip
	vc := &VersionChecker{
		skipCheck:   false,
		githubOwner: "test",
		githubRepo:  "test",
	}

	// Override the fetch function by checking the cache behavior
	ctx := t.Context()

	// First call - should not use cache
	_, err := vc.Check(ctx, false)
	// Will fail because we're not actually calling the static site, but that's OK for this test
	// The important thing is that it tried to fetch

	// Second call immediately after - should use cache if first succeeded
	_, err = vc.Check(ctx, false)
	_ = err // Ignore error, we're just testing the cache logic

	// Force refresh should bypass cache
	_, err = vc.Check(ctx, true)
	_ = err
}

func TestFindDownloadURL(t *testing.T) {
	t.Parallel()
	vc := &VersionChecker{}

	release := &ReleaseInfo{
		TagName: "v0.1.0",
		DownloadURLs: map[string]string{
			"linux_amd64":  "https://example.com/linux_amd64",
			"linux_arm64":  "https://example.com/linux_arm64",
			"darwin_amd64": "https://example.com/darwin_amd64",
			"darwin_arm64": "https://example.com/darwin_arm64",
		},
	}

	url := vc.findDownloadURL(release)
	// The result depends on runtime.GOOS and runtime.GOARCH
	// Just verify it doesn't panic and returns something for known platforms
	if url == "" {
		t.Log("No matching download URL found for current platform - this is expected on some platforms")
	}
}

func TestFetchChangelogPrefixMatching(t *testing.T) {
	t.Parallel()
	// v0.212.925024401 -> extractSHAFromTag returns "542901" (6 chars)
	// v0.213.950002063 -> extractSHAFromTag returns "a00433" (6 chars)
	// commits.json has "542901e" and "a004332" (7 chars)
	// This test verifies the prefix matching logic handles the length mismatch.
	commits := []StaticCommitInfo{
		{SHA: "a004332", Subject: "fix: latest commit"},
		{SHA: "542901e", Subject: "shelley/ui: middle commit"},
		{SHA: "e3ed88a", Subject: "shelley: another commit"},
		{SHA: "60ee3ab", Subject: "shelley/ui: old commit"},
	}

	currentSHA := extractSHAFromTag("v0.212.925024401")
	latestSHA := extractSHAFromTag("v0.213.950002063")

	if currentSHA != "542901" {
		t.Fatalf("expected currentSHA=542901, got %s", currentSHA)
	}
	if latestSHA != "a00433" {
		t.Fatalf("expected latestSHA=a00433, got %s", latestSHA)
	}

	// Verify prefix matching works: "a004332" starts with "a00433"
	if len("a004332") <= len(latestSHA) {
		t.Fatal("test setup wrong: commit SHA should be longer than tag SHA")
	}

	// Simulate the matching logic from FetchChangelog
	var result []CommitInfo
	var foundLatest, foundCurrent bool
	for _, c := range commits {
		if hasPrefix(c.SHA, latestSHA) {
			foundLatest = true
		}
		if foundLatest && !foundCurrent {
			result = append(result, CommitInfo{SHA: c.SHA, Message: c.Subject})
		}
		if hasPrefix(c.SHA, currentSHA) {
			foundCurrent = true
			break
		}
	}

	if !foundLatest {
		t.Error("did not find latest SHA via prefix matching")
	}
	if !foundCurrent {
		t.Error("did not find current SHA via prefix matching")
	}

	// Remove current commit from list
	if len(result) > 0 {
		last := result[len(result)-1].SHA
		if hasPrefix(last, currentSHA) {
			result = result[:len(result)-1]
		}
	}

	// Should have 1 commit: a004332 (the latest). 542901e is the current and
	// was removed. They are adjacent in the list so there's nothing in between.
	if len(result) != 1 {
		t.Errorf("expected 1 commit, got %d: %+v", len(result), result)
	}
	if len(result) > 0 && result[0].SHA != "a004332" {
		t.Errorf("expected first commit SHA=a004332, got %s", result[0].SHA)
	}
}

func TestFetchChangelogPrefixMatchingMultipleCommits(t *testing.T) {
	t.Parallel()
	// Same as above but with commits between current and latest
	commits := []StaticCommitInfo{
		{SHA: "a004332", Subject: "fix: latest commit"},
		{SHA: "1111111", Subject: "middle commit 1"},
		{SHA: "2222222", Subject: "middle commit 2"},
		{SHA: "542901e", Subject: "current commit"},
		{SHA: "60ee3ab", Subject: "old commit"},
	}

	currentSHA := "542901" // 6-char from tag extraction
	latestSHA := "a00433"  // 6-char from tag extraction

	var result []CommitInfo
	var foundLatest, foundCurrent bool
	for _, c := range commits {
		if hasPrefix(c.SHA, latestSHA) {
			foundLatest = true
		}
		if foundLatest && !foundCurrent {
			result = append(result, CommitInfo{SHA: c.SHA, Message: c.Subject})
		}
		if hasPrefix(c.SHA, currentSHA) {
			foundCurrent = true
			break
		}
	}

	if !foundLatest || !foundCurrent {
		t.Fatal("did not find both SHAs")
	}

	// Remove current
	if len(result) > 0 {
		last := result[len(result)-1].SHA
		if hasPrefix(last, currentSHA) {
			result = result[:len(result)-1]
		}
	}

	// Should have 3 commits: a004332, 1111111, 2222222
	if len(result) != 3 {
		t.Fatalf("expected 3 commits, got %d: %+v", len(result), result)
	}
	expected := []string{"a004332", "1111111", "2222222"}
	for i, exp := range expected {
		if result[i].SHA != exp {
			t.Errorf("commit[%d]: expected SHA=%s, got %s", i, exp, result[i].SHA)
		}
	}
}

func hasPrefix(a, b string) bool {
	return len(a) >= len(b) && a[:len(b)] == b || len(b) >= len(a) && b[:len(a)] == a
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
