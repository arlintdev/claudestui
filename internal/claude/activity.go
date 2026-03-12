package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/arlintdev/claudes/internal/instance"
	"github.com/fsnotify/fsnotify"
)

// ActivityWatcher monitors JSONL session files via fsnotify on project
// directories and maintains per-instance activity summaries.
type ActivityWatcher struct {
	mu       sync.RWMutex
	activity map[string]string          // instance name → last activity
	status   map[string]instance.Status // instance name → idle/running from JSONL
	dirs     map[string]string          // instance name → project dir being watched
	names    map[string]string          // instance name → session ID (may be "")
	watched  map[string]bool            // project dirs currently watched
	watcher  *fsnotify.Watcher
}

// NewActivityWatcher creates a watcher. Call Close() when done.
func NewActivityWatcher() (*ActivityWatcher, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("activity watcher: %w", err)
	}
	aw := &ActivityWatcher{
		activity: make(map[string]string),
		status:   make(map[string]instance.Status),
		dirs:     make(map[string]string),
		names:    make(map[string]string),
		watched:  make(map[string]bool),
		watcher:  w,
	}
	go aw.loop()
	return aw, nil
}

// Watch starts monitoring an instance's JSONL activity. Watches the project
// directory so new session files are picked up automatically.
func (aw *ActivityWatcher) Watch(name, dir, sessionID string) {
	projectDir := projectDirForPath(dir)
	if projectDir == "" {
		return
	}

	aw.mu.Lock()
	defer aw.mu.Unlock()

	aw.dirs[name] = projectDir
	aw.names[name] = sessionID

	// Watch the directory if not already
	if !aw.watched[projectDir] {
		if err := aw.watcher.Add(projectDir); err == nil {
			aw.watched[projectDir] = true
		}
	}

	// Read current state immediately
	act, st := aw.readState(projectDir, sessionID)
	aw.activity[name] = act
	aw.status[name] = st
}

// Unwatch stops watching an instance.
func (aw *ActivityWatcher) Unwatch(name string) {
	aw.mu.Lock()
	defer aw.mu.Unlock()
	delete(aw.dirs, name)
	delete(aw.names, name)
	delete(aw.activity, name)
	delete(aw.status, name)
	// Don't remove dir watch — other instances may share it
}

// Get returns the current activity for an instance.
func (aw *ActivityWatcher) Get(name string) string {
	aw.mu.RLock()
	defer aw.mu.RUnlock()
	return aw.activity[name]
}

// All returns a snapshot of all activity.
func (aw *ActivityWatcher) All() map[string]string {
	aw.mu.RLock()
	defer aw.mu.RUnlock()
	out := make(map[string]string, len(aw.activity))
	for k, v := range aw.activity {
		out[k] = v
	}
	return out
}

// Status returns the JSONL-derived status for an instance (idle or running).
// Returns StatusRunning if unknown.
func (aw *ActivityWatcher) Status(name string) instance.Status {
	aw.mu.RLock()
	defer aw.mu.RUnlock()
	if s, ok := aw.status[name]; ok {
		return s
	}
	return instance.StatusRunning
}

// Close shuts down the watcher.
func (aw *ActivityWatcher) Close() {
	aw.watcher.Close()
}

func (aw *ActivityWatcher) loop() {
	for {
		select {
		case event, ok := <-aw.watcher.Events:
			if !ok {
				return
			}
			if event.Has(fsnotify.Write) || event.Has(fsnotify.Create) {
				if strings.HasSuffix(event.Name, ".jsonl") {
					aw.handleFileChange(event.Name)
				}
			}
		case _, ok := <-aw.watcher.Errors:
			if !ok {
				return
			}
		}
	}
}

func (aw *ActivityWatcher) handleFileChange(path string) {
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	sessionID := strings.TrimSuffix(base, ".jsonl")

	aw.mu.Lock()
	defer aw.mu.Unlock()

	// Build set of already-claimed session IDs in this directory
	claimed := make(map[string]bool)
	for _, sid := range aw.names {
		if sid != "" {
			claimed[sid] = true
		}
	}

	// Read file once, extract both activity and status
	act, st := readStateFromFile(path)

	for name, projDir := range aw.dirs {
		if projDir != dir {
			continue
		}
		wantSession := aw.names[name]

		if wantSession == sessionID {
			// Exact match
			if act != "" {
				aw.activity[name] = act
			}
			aw.status[name] = st
		} else if !claimed[sessionID] {
			// Either unbound (wantSession == "") or bound to a stale session.
			// Re-bind if the current session's file is older than the changed file.
			rebind := wantSession == ""
			if !rebind && wantSession != "" {
				oldPath := filepath.Join(dir, wantSession+".jsonl")
				oldInfo, errOld := os.Stat(oldPath)
				newInfo, errNew := os.Stat(path)
				if errOld == nil && errNew == nil && newInfo.ModTime().After(oldInfo.ModTime()) {
					rebind = true
				}
			}
			if rebind {
				aw.names[name] = sessionID
				claimed[sessionID] = true
				if act != "" {
					aw.activity[name] = act
				}
				aw.status[name] = st
			}
		}
	}
}

// readState finds and reads activity + status for an instance.
func (aw *ActivityWatcher) readState(projectDir, sessionID string) (string, instance.Status) {
	var jsonlPath string
	if sessionID != "" {
		jsonlPath = filepath.Join(projectDir, sessionID+".jsonl")
	} else {
		jsonlPath = latestJSONL(projectDir)
	}
	if jsonlPath == "" {
		return "", instance.StatusRunning
	}
	return readStateFromFile(jsonlPath)
}

// readStateFromFile reads the last few lines of a JSONL file and returns
// both the last assistant activity string and the idle/running status.
func readStateFromFile(path string) (string, instance.Status) {
	lines := readLastNLines(path, 15)
	if len(lines) == 0 {
		return "", instance.StatusRunning
	}

	// Status from scanning the last several lines
	status := statusFromLines(lines)

	// Activity comes from the last assistant entry (may not be the last line)
	var activity string
	for i := len(lines) - 1; i >= 0; i-- {
		if a := parseActivity(lines[i]); a != "" {
			activity = a
			break
		}
	}

	return activity, status
}

// statusFromLines determines idle/running by scanning the last N JSONL lines.
//
// Logic: find the last assistant entry with a non-null stop_reason.
//   - "end_turn" → idle (Claude finished, waiting for user)
//   - "tool_use" → idle if no user entry follows (waiting for approval),
//     running if a user entry follows (tool result sent, Claude processing)
//   - No such entry found → running (Claude is streaming or just started)
func statusFromLines(lines []string) instance.Status {
	// Walk backwards to find the last assistant with stop_reason
	for i := len(lines) - 1; i >= 0; i-- {
		var entry activityEntry
		if err := json.Unmarshal([]byte(lines[i]), &entry); err != nil {
			continue
		}

		if entry.Type == "assistant" && entry.Message != nil && entry.Message.StopReason != nil {
			sr := *entry.Message.StopReason

			if sr == "end_turn" {
				return instance.StatusIdle
			}

			if sr == "tool_use" {
				// Check if a user entry (tool_result) follows, AND then
				// another assistant entry after that — means Claude is working.
				// If just a user entry with no assistant after → still idle (tool executing).
				hasUser := false
				hasAssistantAfter := false
				for j := i + 1; j < len(lines); j++ {
					var next activityEntry
					if json.Unmarshal([]byte(lines[j]), &next) != nil {
						continue
					}
					if next.Type == "user" {
						hasUser = true
					}
					if next.Type == "assistant" && hasUser {
						hasAssistantAfter = true
						break
					}
				}
				if hasUser && hasAssistantAfter {
					return instance.StatusRunning
				}
				return instance.StatusIdle
			}

			// Other stop reasons (shouldn't happen normally)
			return instance.StatusRunning
		}
	}

	return instance.StatusRunning
}

// activityEntry is the minimal JSONL structure we need for activity parsing.
type activityEntry struct {
	Type    string `json:"type"`
	Message *struct {
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text,omitempty"`
			Name  string          `json:"name,omitempty"`
			Input json.RawMessage `json:"input,omitempty"`
		} `json:"content"`
		StopReason *string `json:"stop_reason"`
	} `json:"message"`
}

// toolInput extracts specific fields from tool_use input for display.
type toolInput struct {
	FilePath string `json:"file_path"`
	Command  string `json:"command"`
	Pattern  string `json:"pattern"`
	Query    string `json:"query"`
	URL      string `json:"url"`
	Prompt   string `json:"prompt"`
}

// parseActivity converts a JSONL line into a human-readable activity string.
// Returns "" if the line is not an assistant entry with useful content.
func parseActivity(line string) string {
	var entry activityEntry
	if err := json.Unmarshal([]byte(line), &entry); err != nil {
		return ""
	}

	if entry.Type != "assistant" || entry.Message == nil {
		return ""
	}

	for _, c := range entry.Message.Content {
		switch c.Type {
		case "tool_use":
			return formatToolUse(c.Name, c.Input)
		case "text":
			text := strings.TrimSpace(c.Text)
			if text != "" {
				if idx := strings.IndexByte(text, '\n'); idx >= 0 {
					text = text[:idx]
				}
				return text
			}
		}
	}
	return ""
}

func formatToolUse(name string, raw json.RawMessage) string {
	var input toolInput
	_ = json.Unmarshal(raw, &input)

	switch name {
	case "Read":
		return fmt.Sprintf("Read %s", shortPath(input.FilePath))
	case "Edit":
		return fmt.Sprintf("Edit %s", shortPath(input.FilePath))
	case "Write":
		return fmt.Sprintf("Write %s", shortPath(input.FilePath))
	case "Bash":
		cmd := input.Command
		if cmd == "" {
			return "Bash"
		}
		if idx := strings.IndexByte(cmd, '\n'); idx >= 0 {
			cmd = cmd[:idx]
		}
		if len(cmd) > 60 {
			cmd = cmd[:57] + "..."
		}
		return fmt.Sprintf("$ %s", cmd)
	case "Glob":
		return fmt.Sprintf("Glob %s", input.Pattern)
	case "Grep":
		return fmt.Sprintf("Grep %s", input.Pattern)
	case "WebFetch":
		return fmt.Sprintf("Fetch %s", input.URL)
	case "WebSearch":
		return fmt.Sprintf("Search %q", input.Query)
	case "Agent":
		return fmt.Sprintf("Agent: %s", input.Prompt)
	default:
		return name
	}
}

// shortPath trims a file path to just the last 2 components.
func shortPath(p string) string {
	parts := strings.Split(p, "/")
	if len(parts) <= 2 {
		return p
	}
	return "…/" + strings.Join(parts[len(parts)-2:], "/")
}
