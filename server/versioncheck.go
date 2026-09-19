package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	neturl "net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fynelabs/selfupdate"

	"shelley.exe.dev/version"
)

// VersionChecker checks for new versions of Shelley from GitHub releases.
type VersionChecker struct {
	upgradeMu   sync.Mutex // Protects binary replacement across manual and automatic upgrades.
	mu          sync.Mutex
	lastCheck   time.Time
	cachedInfo  *VersionInfo
	skipCheck   bool
	githubOwner string
	githubRepo  string
}

// VersionInfo contains version check results.
type VersionInfo struct {
	CurrentVersion      string       `json:"current_version"`
	CurrentTag          string       `json:"current_tag,omitempty"`
	CurrentCommit       string       `json:"current_commit,omitempty"`
	CurrentCommitTime   string       `json:"current_commit_time,omitempty"`
	LatestVersion       string       `json:"latest_version,omitempty"`
	LatestTag           string       `json:"latest_tag,omitempty"`
	PublishedAt         time.Time    `json:"published_at,omitempty"`
	HasUpdate           bool         `json:"has_update"`    // True if minor version is newer (for showing upgrade button)
	ShouldNotify        bool         `json:"should_notify"` // True if should show red dot (newer + 5 days old)
	Customized          bool         `json:"customized"`    // True for user-customized builds (see customizing-shelley skill)
	CustomizationDir    string       `json:"customization_dir,omitempty"`
	CustomCommits       []CommitInfo `json:"custom_commits,omitempty"` // Commits the customization branch carries on top of mainline
	DownloadURL         string       `json:"download_url,omitempty"`
	ExecutablePath      string       `json:"executable_path,omitempty"`
	Commits             []CommitInfo `json:"commits,omitempty"`
	CheckedAt           time.Time    `json:"checked_at"`
	Error               string       `json:"error,omitempty"`
	RunningUnderSystemd bool         `json:"running_under_systemd"` // True if INVOCATION_ID env var is set (systemd)
	ReleaseInfo         *ReleaseInfo `json:"-"`                     // Internal, not exposed to JSON

	// Headless shell (browser) version info
	HeadlessShellCurrent string `json:"headless_shell_current,omitempty"` // e.g. "Chromium 141.0.7390.55"
	HeadlessShellLatest  string `json:"headless_shell_latest,omitempty"`  // e.g. "Chromium 147.0.7727.24"
	HeadlessShellUpdate  bool   `json:"headless_shell_update"`            // True if latest > current
}

// CommitInfo represents a commit in the changelog.
type CommitInfo struct {
	SHA     string    `json:"sha"`
	Message string    `json:"message"`
	Author  string    `json:"author"`
	Date    time.Time `json:"date"`
}

// ReleaseInfo represents release metadata.
type ReleaseInfo struct {
	TagName              string            `json:"tag_name"`
	Version              string            `json:"version"`
	Commit               string            `json:"commit"`
	CommitFull           string            `json:"commit_full"`
	CommitTime           string            `json:"commit_time"`
	PublishedAt          string            `json:"published_at"`
	DownloadURLs         map[string]string `json:"download_urls"`
	ChecksumsURL         string            `json:"checksums_url"`
	HeadlessShellVersion string            `json:"headless_shell_version,omitempty"`
	HeadlessShellURLs    map[string]string `json:"headless_shell_urls,omitempty"`
}

// StaticCommitInfo represents a commit from commits.json.
type StaticCommitInfo struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
}

const (
	// staticMetadataURL is the base URL for version metadata on GitHub Pages.
	// This avoids GitHub API rate limits.
	staticMetadataURL = "https://boldsoftware.github.io/shelley"
)

// NewVersionChecker creates a new version checker.
func NewVersionChecker() *VersionChecker {
	skipCheck := os.Getenv("SHELLEY_SKIP_VERSION_CHECK") == "true"
	return &VersionChecker{
		skipCheck:   skipCheck,
		githubOwner: "boldsoftware",
		githubRepo:  "shelley",
	}
}

// CustomizationDir returns the canonical checkout location for user-customized
// Shelley builds (see the customizing-shelley skill).
func CustomizationDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "shelley", "shelley-customization")
}

// existingCustomizationDir returns CustomizationDir if it exists on disk,
// else "". The version dialog uses it as the cwd for rebase-upgrade
// conversations, which must not start in a nonexistent directory.
func existingCustomizationDir() string {
	dir := CustomizationDir()
	if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
		return dir
	}
	return ""
}

// customCommits lists the commits the customization checkout carries on top
// of mainline (reachable from HEAD but not origin/main), newest first. The
// list is display-only; errors (no checkout, mid-rebase, missing remote ref)
// just yield an empty list.
func customCommits(dir string) []CommitInfo {
	if dir == "" {
		return nil
	}
	out, err := exec.Command("git", "-C", dir, "log", "-n", "50",
		"--format=%h%x1f%s%x1f%an%x1f%aI", "origin/main..HEAD").Output()
	if err != nil {
		return nil
	}
	var commits []CommitInfo
	for line := range strings.Lines(string(out)) {
		sha, rest, _ := strings.Cut(strings.TrimSuffix(line, "\n"), "\x1f")
		msg, rest, _ := strings.Cut(rest, "\x1f")
		author, dateStr, _ := strings.Cut(rest, "\x1f")
		if sha == "" || msg == "" {
			continue
		}
		date, _ := time.Parse(time.RFC3339, dateStr)
		commits = append(commits, CommitInfo{SHA: sha, Message: msg, Author: author, Date: date})
	}
	return commits
}

// baseVersionInfo builds a VersionInfo populated from the running build,
// before any release-metadata fetch.
func baseVersionInfo() *VersionInfo {
	currentInfo := version.GetInfo()
	execPath, _ := os.Executable()
	info := &VersionInfo{
		CurrentVersion:      currentInfo.Version,
		CurrentTag:          currentInfo.Tag,
		CurrentCommit:       currentInfo.Commit,
		CurrentCommitTime:   currentInfo.CommitTime,
		Customized:          currentInfo.Customized,
		CustomizationDir:    existingCustomizationDir(),
		ExecutablePath:      execPath,
		CheckedAt:           time.Now(),
		RunningUnderSystemd: os.Getenv("INVOCATION_ID") != "",
	}
	if info.Customized {
		info.CustomCommits = customCommits(info.CustomizationDir)
	}
	return info
}

// Check checks for a new version, using the cache if still valid.
func (vc *VersionChecker) Check(ctx context.Context, forceRefresh bool) (*VersionInfo, error) {
	if vc.skipCheck {
		return baseVersionInfo(), nil
	}

	vc.mu.Lock()
	defer vc.mu.Unlock()

	// Return cached info if still valid (6 hours) and not forcing refresh
	if !forceRefresh && vc.cachedInfo != nil && time.Since(vc.lastCheck) < 6*time.Hour {
		return vc.cachedInfo, nil
	}

	info, err := vc.fetchVersionInfo(ctx)
	if err != nil {
		// On error, return current version info with error
		info := baseVersionInfo()
		info.Error = err.Error()
		return info, nil
	}

	vc.cachedInfo = info
	vc.lastCheck = time.Now()
	return info, nil
}

// fetchVersionInfo fetches the latest release info from GitHub Pages.
func (vc *VersionChecker) fetchVersionInfo(ctx context.Context) (*VersionInfo, error) {
	info := baseVersionInfo()

	// Fetch latest release from static metadata
	latestRelease, err := vc.fetchLatestRelease(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch latest release: %w", err)
	}

	info.LatestTag = latestRelease.TagName
	info.LatestVersion = latestRelease.TagName
	info.ReleaseInfo = latestRelease

	// Parse the published_at time, falling back to commit_time if published_at
	// can't be parsed (e.g. older releases where it contained the tag message).
	if publishedAt, err := time.Parse(time.RFC3339, latestRelease.PublishedAt); err == nil {
		info.PublishedAt = publishedAt
	} else if commitTime, err := time.Parse(time.RFC3339, latestRelease.CommitTime); err == nil {
		info.PublishedAt = commitTime
	}

	// Find the download URL for the current platform
	info.DownloadURL = vc.findDownloadURL(latestRelease)

	// Check if latest has a newer minor version
	info.HasUpdate = vc.isNewerMinor(info.CurrentTag, latestRelease.TagName)

	// Headless shell version info
	info.HeadlessShellCurrent = getLocalHeadlessShellVersion(ctx)
	if latestRelease.HeadlessShellVersion != "" && info.HeadlessShellCurrent != "" {
		info.HeadlessShellLatest = latestRelease.HeadlessShellVersion
		info.HeadlessShellUpdate = headlessShellHasUpdate(info.HeadlessShellCurrent, info.HeadlessShellLatest)
	}

	// For ShouldNotify, compare commit times if we have an update
	if info.HasUpdate && info.CurrentCommitTime != "" {
		currentTime, err1 := time.Parse(time.RFC3339, info.CurrentCommitTime)
		latestTime, err2 := time.Parse(time.RFC3339, latestRelease.CommitTime)
		if err1 == nil && err2 == nil {
			// Show notification if the latest release is 5+ days newer than current
			timeBetween := latestTime.Sub(currentTime)
			info.ShouldNotify = timeBetween >= 5*24*time.Hour
		} else {
			// Can't parse times, just notify if there's an update
			info.ShouldNotify = true
		}
	}

	return info, nil
}

// FetchChangelog fetches the commits between current and latest versions.
func (vc *VersionChecker) FetchChangelog(ctx context.Context, currentTag, latestTag string) ([]CommitInfo, error) {
	if currentTag == "" || latestTag == "" {
		return nil, nil
	}

	// Cache-bust with the latest tag. GitHub Pages sits behind a CDN with
	// cache-control: max-age=600, and release.json and commits.json are
	// cached independently. Right after a release, a fresh release.json can
	// pair with a stale commits.json that doesn't contain the new tag's
	// commit yet, making the changelog come up empty. The tag is a perfect
	// cache key: a new tag implies a redeployed commits.json containing it.
	url := staticMetadataURL + "/commits.json?v=" + neturl.QueryEscape(latestTag)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Shelley-VersionChecker")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("static commits returned status %d", resp.StatusCode)
	}

	var staticCommits []StaticCommitInfo
	if err := json.NewDecoder(resp.Body).Decode(&staticCommits); err != nil {
		return nil, err
	}

	// Extract short SHAs from tags (tags are v0.X.YSHA where SHA is octal-encoded)
	currentSHA := extractSHAFromTag(currentTag)
	latestSHA := extractSHAFromTag(latestTag)

	if currentSHA == "" || latestSHA == "" {
		return nil, fmt.Errorf("could not extract SHAs from tags")
	}

	// Find the range of commits between current and latest.
	// Use prefix matching since the tag encodes 6-char hex SHAs but
	// commits.json may use the default git short SHA length (7+ chars).
	var commits []CommitInfo
	var foundLatest, foundCurrent bool

	for _, c := range staticCommits {
		if strings.HasPrefix(c.SHA, latestSHA) || strings.HasPrefix(latestSHA, c.SHA) {
			foundLatest = true
		}
		if foundLatest && !foundCurrent {
			commits = append(commits, CommitInfo{
				SHA:     c.SHA,
				Message: c.Subject,
			})
		}
		if strings.HasPrefix(c.SHA, currentSHA) || strings.HasPrefix(currentSHA, c.SHA) {
			foundCurrent = true
			break
		}
	}

	// If we didn't find both SHAs, the commits might be too old (outside 500 range)
	if !foundLatest || !foundCurrent {
		return nil, fmt.Errorf("commits not found in static list (current=%s, latest=%s)", currentSHA, latestSHA)
	}

	// Remove the current commit itself from the list (we want commits after current)
	if len(commits) > 0 {
		lastSHA := commits[len(commits)-1].SHA
		if strings.HasPrefix(lastSHA, currentSHA) || strings.HasPrefix(currentSHA, lastSHA) {
			commits = commits[:len(commits)-1]
		}
	}

	return commits, nil
}

// extractSHAFromTag extracts the short commit SHA from a version tag.
// Tags are formatted as v0.COUNT.9OCTAL where OCTAL is the SHA in octal.
func extractSHAFromTag(tag string) string {
	// Tag format: v0.178.9XXXXX where XXXXX is octal-encoded 6-char hex SHA
	if len(tag) < 3 || tag[0] != 'v' {
		return ""
	}

	// Find the last dot
	lastDot := -1
	for i := len(tag) - 1; i >= 0; i-- {
		if tag[i] == '.' {
			lastDot = i
			break
		}
	}
	if lastDot == -1 {
		return ""
	}

	// Extract the patch part (9XXXXX)
	patch := tag[lastDot+1:]
	if len(patch) < 2 || patch[0] != '9' {
		return ""
	}

	// Parse the octal number after '9'
	octal := patch[1:]
	var hexVal uint64
	for _, c := range octal {
		if c < '0' || c > '7' {
			return ""
		}
		hexVal = hexVal*8 + uint64(c-'0')
	}

	// Convert back to 6-char hex SHA (short SHA)
	return fmt.Sprintf("%06x", hexVal)
}

const headlessShellPath = "/headless-shell/headless-shell"

// getLocalHeadlessShellVersion runs headless-shell --version and returns the output.
// Returns empty string if the binary doesn't exist or fails.
func getLocalHeadlessShellVersion(ctx context.Context) string {
	cmd := exec.CommandContext(ctx, headlessShellPath, "--version")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// headlessShellHasUpdate compares two version strings like "Chromium 141.0.7390.55"
// and returns true if latest is newer than current.
func headlessShellHasUpdate(current, latest string) bool {
	currentVer := strings.TrimPrefix(current, "Chromium ")
	latestVer := strings.TrimPrefix(latest, "Chromium ")
	if currentVer == "" || latestVer == "" {
		return false
	}

	currentParts := strings.Split(currentVer, ".")
	latestParts := strings.Split(latestVer, ".")

	for i := 0; i < len(currentParts) && i < len(latestParts); i++ {
		c, err1 := strconv.Atoi(currentParts[i])
		l, err2 := strconv.Atoi(latestParts[i])
		if err1 != nil || err2 != nil {
			return false
		}
		if l > c {
			return true
		}
		if l < c {
			return false
		}
	}
	return false
}

// findHeadlessShellURL finds the headless-shell download URL for the current platform.
func findHeadlessShellURL(release *ReleaseInfo) string {
	key := fmt.Sprintf("%s_%s", runtime.GOOS, runtime.GOARCH)
	if url, ok := release.HeadlessShellURLs[key]; ok {
		return url
	}
	return ""
}

// fetchLatestRelease fetches the latest release info from GitHub Pages.
func (vc *VersionChecker) fetchLatestRelease(ctx context.Context) (*ReleaseInfo, error) {
	url := staticMetadataURL + "/release.json"

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Shelley-VersionChecker")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch release info: status %d", resp.StatusCode)
	}

	var release ReleaseInfo
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, err
	}

	return &release, nil
}

// findDownloadURL finds the appropriate download URL for the current platform.
func (vc *VersionChecker) findDownloadURL(release *ReleaseInfo) string {
	key := fmt.Sprintf("%s_%s", runtime.GOOS, runtime.GOARCH)
	if url, ok := release.DownloadURLs[key]; ok {
		return url
	}
	return ""
}

// isNewerMinor checks if latest has a higher minor version than current.
func (vc *VersionChecker) isNewerMinor(currentTag, latestTag string) bool {
	currentMinor := parseMinorVersion(currentTag)
	latestMinor := parseMinorVersion(latestTag)
	return latestMinor > currentMinor
}

// parseMinorVersion extracts the X from v0.X.Y format.
func parseMinorVersion(tag string) int {
	if len(tag) < 2 || tag[0] != 'v' {
		return 0
	}

	// Skip 'v'
	s := tag[1:]

	// Find first dot
	firstDot := -1
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			firstDot = i
			break
		}
	}
	if firstDot == -1 {
		return 0
	}

	// Skip major version and dot
	s = s[firstDot+1:]

	// Parse minor version
	var minor int
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			minor = minor*10 + int(s[i]-'0')
		} else {
			break
		}
	}

	return minor
}

// DoUpgrade downloads and applies the update with checksum verification.
func (vc *VersionChecker) DoUpgrade(ctx context.Context) error {
	if !vc.upgradeMu.TryLock() {
		return fmt.Errorf("upgrade already in progress")
	}
	defer vc.upgradeMu.Unlock()

	if vc.skipCheck {
		return fmt.Errorf("version checking is disabled")
	}

	if version.GetInfo().Customized {
		return fmt.Errorf("this is a customized build; upgrade by rebasing %s onto origin/main (see the customizing-shelley skill)", CustomizationDir())
	}

	// Get cached info or fetch fresh
	info, err := vc.Check(ctx, false)
	if err != nil {
		return fmt.Errorf("failed to check version: %w", err)
	}

	if !info.HasUpdate {
		return fmt.Errorf("no update available")
	}

	if info.DownloadURL == "" {
		return fmt.Errorf("no download URL for %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	if info.ReleaseInfo == nil {
		return fmt.Errorf("no release info available")
	}

	// Find and download checksums.txt
	expectedChecksum, err := vc.fetchExpectedChecksum(ctx, info.ReleaseInfo)
	if err != nil {
		return fmt.Errorf("failed to fetch checksum: %w", err)
	}

	// Download the binary
	resp, err := http.Get(info.DownloadURL)
	if err != nil {
		return fmt.Errorf("failed to download update: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned status %d", resp.StatusCode)
	}

	// Read the entire binary to verify checksum before applying
	binaryData, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read update: %w", err)
	}

	// Verify checksum
	actualChecksum := sha256.Sum256(binaryData)
	actualChecksumHex := hex.EncodeToString(actualChecksum[:])

	if actualChecksumHex != expectedChecksum {
		return fmt.Errorf("checksum mismatch: expected %s, got %s", expectedChecksum, actualChecksumHex)
	}

	// Apply the update
	err = selfupdate.Apply(bytes.NewReader(binaryData), selfupdate.Options{})
	if err == nil {
		return nil
	}

	// Check if the error is permission-related and sudo is available
	if !isPermissionError(err) {
		return fmt.Errorf("failed to apply update: %w", err)
	}

	if !isSudoAvailable() {
		return fmt.Errorf("failed to apply update (no write permission and sudo not available): %w", err)
	}

	// Fall back to sudo-based upgrade
	return vc.doSudoUpgrade(binaryData)
}

// isPermissionError checks if the error is related to file permissions.
func isPermissionError(err error) bool {
	return errors.Is(err, fs.ErrPermission) || errors.Is(err, os.ErrPermission)
}

// doSudoUpgrade performs the upgrade using sudo when the binary isn't writable.
func (vc *VersionChecker) doSudoUpgrade(binaryData []byte) error {
	// Get the path to the current executable
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}

	// Write the new binary to a temp file
	tmpFile, err := os.CreateTemp("", "shelley-upgrade-*")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	if _, err := tmpFile.Write(binaryData); err != nil {
		tmpFile.Close()
		return fmt.Errorf("failed to write temp file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("failed to close temp file: %w", err)
	}

	// Make the temp file executable
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		return fmt.Errorf("failed to chmod temp file: %w", err)
	}

	// Use sudo to install the new binary. We can't cp over a running binary ("Text file busy"),
	// so we cp to a .new file and then mv (which is atomic and works on running binaries).
	newPath := exePath + ".new"
	oldPath := exePath + ".old"

	// Copy new binary to .new location
	cmd := exec.Command("sudo", "cp", tmpPath, newPath)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to copy new binary: %w: %s", err, output)
	}

	// Copy ownership and permissions from original
	cmd = exec.Command("sudo", "chown", "--reference="+exePath, newPath)
	if output, err := cmd.CombinedOutput(); err != nil {
		exec.Command("sudo", "rm", "-f", newPath).Run()
		return fmt.Errorf("failed to set ownership: %w: %s", err, output)
	}

	cmd = exec.Command("sudo", "chmod", "--reference="+exePath, newPath)
	if output, err := cmd.CombinedOutput(); err != nil {
		exec.Command("sudo", "rm", "-f", newPath).Run()
		return fmt.Errorf("failed to set permissions: %w: %s", err, output)
	}

	// Rename old binary to .old (backup)
	cmd = exec.Command("sudo", "mv", exePath, oldPath)
	if output, err := cmd.CombinedOutput(); err != nil {
		exec.Command("sudo", "rm", "-f", newPath).Run()
		return fmt.Errorf("failed to backup old binary: %w: %s", err, output)
	}

	// Rename .new to target (atomic replacement)
	cmd = exec.Command("sudo", "mv", newPath, exePath)
	if output, err := cmd.CombinedOutput(); err != nil {
		// Try to restore the old binary
		exec.Command("sudo", "mv", oldPath, exePath).Run()
		return fmt.Errorf("failed to install new binary: %w: %s", err, output)
	}

	// Remove the backup
	cmd = exec.Command("sudo", "rm", "-f", oldPath)
	cmd.Run() // Best effort, ignore errors

	return nil
}

// fetchExpectedChecksum downloads checksums.txt and extracts the expected checksum for our binary.
func (vc *VersionChecker) fetchExpectedChecksum(ctx context.Context, release *ReleaseInfo) (string, error) {
	checksumURL := release.ChecksumsURL
	if checksumURL == "" {
		return "", fmt.Errorf("checksums.txt URL not found in release")
	}

	// Download checksums.txt
	req, err := http.NewRequestWithContext(ctx, "GET", checksumURL, nil)
	if err != nil {
		return "", err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to download checksums: status %d", resp.StatusCode)
	}

	// Parse checksums.txt (format: "checksum  filename")
	expectedBinaryName := fmt.Sprintf("shelley_%s_%s", runtime.GOOS, runtime.GOARCH)

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			checksum := parts[0]
			filename := parts[1]
			if filename == expectedBinaryName {
				return checksum, nil
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("error reading checksums: %w", err)
	}

	return "", fmt.Errorf("checksum for %s not found in checksums.txt", expectedBinaryName)
}
