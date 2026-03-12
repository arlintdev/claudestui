package claude

import (
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/arlintdev/claudes/internal/instance"
)

// encodeProjectPath converts a directory path to Claude's encoded project dir name.
// This is the inverse of decodeProjectPath: "/Users/austin/panes" → "-Users-austin-panes".
func encodeProjectPath(dir string) string {
	if dir == "" {
		return ""
	}
	// Replace leading / and all / with -
	return "-" + strings.ReplaceAll(strings.TrimPrefix(dir, "/"), "/", "-")
}

// StatusFromJSONL determines whether Claude is idle or running by reading
// the last line of the session's JSONL file. If sessionID is provided, it
// reads that specific file; otherwise falls back to the most recent JSONL.
func StatusFromJSONL(dir, sessionID string) instance.Status {
	projectDir := projectDirForPath(dir)
	if projectDir == "" {
		return instance.StatusRunning // can't determine, assume running
	}

	var jsonlPath string
	if sessionID != "" {
		jsonlPath = filepath.Join(projectDir, sessionID+".jsonl")
		if _, err := os.Stat(jsonlPath); err != nil {
			jsonlPath = "" // fall back
		}
	}
	if jsonlPath == "" {
		jsonlPath = latestJSONL(projectDir)
	}

	lines := readLastNLines(jsonlPath, 15)
	if len(lines) == 0 {
		return instance.StatusRunning
	}

	return statusFromLines(lines)
}

// ActiveSessionID returns the session ID (filename stem) of the most recently
// modified JSONL file for the given working directory.
func ActiveSessionID(dir string) string {
	projectDir := projectDirForPath(dir)
	if projectDir == "" {
		return ""
	}
	path := latestJSONL(projectDir)
	if path == "" {
		return ""
	}
	base := filepath.Base(path)
	return strings.TrimSuffix(base, ".jsonl")
}

// projectDirForPath returns the Claude projects directory for a working directory.
func projectDirForPath(dir string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	encoded := encodeProjectPath(dir)
	if encoded == "" {
		return ""
	}
	projDir := filepath.Join(home, ".claude", "projects", encoded)
	if info, err := os.Stat(projDir); err != nil || !info.IsDir() {
		return ""
	}
	return projDir
}

// latestJSONL finds the most recently modified .jsonl file in a directory.
func latestJSONL(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}

	var newest string
	var newestTime int64

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if t := info.ModTime().UnixNano(); t > newestTime {
			newestTime = t
			newest = filepath.Join(dir, e.Name())
		}
	}
	return newest
}

// readLastNLines reads the last n non-empty lines of a file by seeking from the end.
func readLastNLines(path string, n int) []string {
	if path == "" || n <= 0 {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil || stat.Size() == 0 {
		return nil
	}

	size := stat.Size()
	buf := make([]byte, 1)
	var lines []string
	var current []byte

	for pos := size - 1; pos >= 0; pos-- {
		if _, err := f.Seek(pos, io.SeekStart); err != nil {
			break
		}
		if _, err := f.Read(buf); err != nil {
			break
		}
		if buf[0] == '\n' {
			if len(current) > 0 {
				lines = append(lines, string(current))
				current = nil
				if len(lines) >= n {
					break
				}
			}
			continue
		}
		current = append([]byte{buf[0]}, current...)
	}
	if len(current) > 0 && len(lines) < n {
		lines = append(lines, string(current))
	}

	// Reverse so oldest is first
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	return lines
}

// readLastLine reads the last non-empty line of a file by seeking from the end.
func readLastLine(path string) string {
	if path == "" {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil || stat.Size() == 0 {
		return ""
	}

	// Read backwards from end to find last newline
	size := stat.Size()
	buf := make([]byte, 1)
	var line []byte

	for pos := size - 1; pos >= 0; pos-- {
		if _, err := f.Seek(pos, io.SeekStart); err != nil {
			return ""
		}
		if _, err := f.Read(buf); err != nil {
			return ""
		}
		if buf[0] == '\n' {
			if len(line) > 0 {
				// Found the start of the last line
				break
			}
			continue // trailing newline, skip
		}
		line = append([]byte{buf[0]}, line...)
	}

	return string(line)
}
