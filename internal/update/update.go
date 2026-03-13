package update

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	releasesURL = "https://api.github.com/repos/arlintdev/claudes/releases/latest"
	checkTimeout = 5 * time.Second
)

// Release describes an available update.
type Release struct {
	Version     string // e.g. "v0.5.0"
	DownloadURL string // asset URL for current OS/arch
}

// ghRelease is the GitHub API response subset we need.
type ghRelease struct {
	TagName string    `json:"tag_name"`
	Assets  []ghAsset `json:"assets"`
}

type ghAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// CheckLatest queries GitHub for the latest release. Returns nil if the
// current version is already up-to-date or if the check fails (network
// errors are silently ignored so startup is never blocked).
func CheckLatest(currentVersion string) *Release {
	if currentVersion == "" || currentVersion == "dev" {
		return nil
	}

	client := &http.Client{Timeout: checkTimeout}
	resp, err := client.Get(releasesURL)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil
	}

	var rel ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil
	}

	if rel.TagName == "" || !isNewer(rel.TagName, currentVersion) {
		return nil
	}

	url := pickAsset(rel.Assets)
	if url == "" {
		return nil
	}

	return &Release{
		Version:     rel.TagName,
		DownloadURL: url,
	}
}

// Apply downloads the release binary and replaces the current executable.
func Apply(downloadURL string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("find executable: %w", err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return fmt.Errorf("resolve executable: %w", err)
	}

	resp, err := http.Get(downloadURL)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned %s", resp.Status)
	}

	// Write to a temp file in the same directory (ensures same filesystem for rename)
	dir := filepath.Dir(exe)
	tmp, err := os.CreateTemp(dir, ".claudes-update-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()

	if _, err := io.Copy(tmp, resp.Body); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write binary: %w", err)
	}
	tmp.Close()

	// Match permissions of the original binary
	info, err := os.Stat(exe)
	if err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("stat executable: %w", err)
	}
	if err := os.Chmod(tmpPath, info.Mode()); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("chmod: %w", err)
	}

	// Atomic replace: rename over the existing binary
	if err := os.Rename(tmpPath, exe); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("replace binary: %w", err)
	}

	return nil
}

// pickAsset finds the download URL matching the current OS and architecture.
func pickAsset(assets []ghAsset) string {
	suffix := fmt.Sprintf("%s-%s", runtime.GOOS, runtime.GOARCH)
	for _, a := range assets {
		if strings.Contains(a.Name, suffix) {
			return a.BrowserDownloadURL
		}
	}
	return ""
}

// isNewer returns true if latest is a newer version than current.
// Both should be "vX.Y.Z" strings. Simple lexicographic comparison
// works for semver when both use the same "v" prefix format.
func isNewer(latest, current string) bool {
	l := parseVersion(latest)
	c := parseVersion(current)
	if l == nil || c == nil {
		return latest != current
	}
	for i := 0; i < 3; i++ {
		if l[i] > c[i] {
			return true
		}
		if l[i] < c[i] {
			return false
		}
	}
	return false
}

// parseVersion parses "vX.Y.Z" into [X, Y, Z]. Returns nil on failure.
func parseVersion(v string) []int {
	v = strings.TrimPrefix(v, "v")
	parts := strings.SplitN(v, ".", 3)
	if len(parts) != 3 {
		return nil
	}
	var nums []int
	for _, p := range parts {
		n := 0
		for _, ch := range p {
			if ch < '0' || ch > '9' {
				return nil
			}
			n = n*10 + int(ch-'0')
		}
		nums = append(nums, n)
	}
	return nums
}
