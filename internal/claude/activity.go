package claude

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/arlintdev/claudes/internal/instance"
	"github.com/fsnotify/fsnotify"
)

// ActivityKind describes the specific type of activity Claude is performing.
type ActivityKind int

const (
	ActivityNone     ActivityKind = iota
	ActivityThinking              // generating text, no tool
	ActivityReading               // Read tool
	ActivityWriting               // Edit, Write, NotebookEdit
	ActivityRunning               // Bash
	ActivitySearching             // Glob, Grep
	ActivityBrowsing              // WebFetch, WebSearch
	ActivitySpawning              // Agent, subagent
	ActivityWaiting               // end_turn, waiting for user
)

// String returns the display label for the activity kind.
func (k ActivityKind) String() string {
	switch k {
	case ActivityThinking:
		return "Thinking"
	case ActivityReading:
		return "Reading"
	case ActivityWriting:
		return "Writing"
	case ActivityRunning:
		return "Running"
	case ActivitySearching:
		return "Searching"
	case ActivityBrowsing:
		return "Browsing"
	case ActivitySpawning:
		return "Spawning"
	case ActivityWaiting:
		return "Waiting"
	default:
		return ""
	}
}

// toolToActivityKind maps a tool name to an activity kind.
func toolToActivityKind(name string) ActivityKind {
	switch name {
	case "Read":
		return ActivityReading
	case "Edit", "Write", "NotebookEdit":
		return ActivityWriting
	case "Bash":
		return ActivityRunning
	case "Glob", "Grep":
		return ActivitySearching
	case "WebFetch", "WebSearch":
		return ActivityBrowsing
	case "Agent":
		return ActivitySpawning
	default:
		return ActivityThinking
	}
}

// shortenModel converts a full model ID to a short display label.
func shortenModel(model string) string {
	switch {
	case strings.Contains(model, "opus"):
		return "opus"
	case strings.Contains(model, "sonnet"):
		return "sonnet"
	case strings.Contains(model, "haiku"):
		return "haiku"
	case model != "":
		return model
	default:
		return ""
	}
}

// ActivityState holds enriched activity data for a single instance.
type ActivityState struct {
	Text       string       // "Read internal/tui/app.go"
	Kind       ActivityKind // ActivityReading
	Model      string       // "sonnet" (shortened)
	CostUSD    float64      // cumulative
	TokensIn   int64        // cumulative
	TokensOut  int64        // cumulative
	Timestamps []time.Time  // entry timestamps for sparkline
}

// ActivityWatcher monitors JSONL session files via fsnotify on project
// directories and maintains per-instance activity summaries.
type ActivityWatcher struct {
	mu       sync.RWMutex
	activity map[string]string          // instance name → last activity
	status   map[string]instance.Status // instance name → idle/running from JSONL
	states   map[string]*ActivityState  // instance name → enriched state
	offsets  map[string]int64           // JSONL path → last scanned byte offset
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
		states:   make(map[string]*ActivityState),
		offsets:  make(map[string]int64),
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

	// Initialize enriched state with full file scan for metrics
	jsonlPath := aw.resolveJSONLPath(projectDir, sessionID)
	if jsonlPath != "" {
		state := readActivityState(jsonlPath)
		aw.scanMetricsFrom(jsonlPath, 0, state)
		aw.states[name] = state
	}
}

// Unwatch stops watching an instance.
func (aw *ActivityWatcher) Unwatch(name string) {
	aw.mu.Lock()
	defer aw.mu.Unlock()
	delete(aw.dirs, name)
	delete(aw.names, name)
	delete(aw.activity, name)
	delete(aw.status, name)
	delete(aw.states, name)
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

// State returns the enriched activity state for an instance.
func (aw *ActivityWatcher) State(name string) *ActivityState {
	aw.mu.RLock()
	defer aw.mu.RUnlock()
	return aw.states[name]
}

// AllStates returns a snapshot of all enriched activity states.
func (aw *ActivityWatcher) AllStates() map[string]*ActivityState {
	aw.mu.RLock()
	defer aw.mu.RUnlock()
	out := make(map[string]*ActivityState, len(aw.states))
	for k, v := range aw.states {
		cp := *v
		if len(v.Timestamps) > 0 {
			cp.Timestamps = make([]time.Time, len(v.Timestamps))
			copy(cp.Timestamps, v.Timestamps)
		}
		out[k] = &cp
	}
	return out
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

	// Read enriched state (activity kind, model) from last lines
	state := readActivityState(path)

	for name, projDir := range aw.dirs {
		if projDir != dir {
			continue
		}
		wantSession := aw.names[name]

		updateState := func() {
			if act != "" {
				aw.activity[name] = act
			}
			aw.status[name] = st
			// Carry over cumulative metrics + timestamps from existing state
			existing := aw.states[name]
			if existing != nil {
				state.CostUSD = existing.CostUSD
				state.TokensIn = existing.TokensIn
				state.TokensOut = existing.TokensOut
				state.Timestamps = existing.Timestamps
			}
			aw.scanMetricsFrom(path, aw.offsets[path], state)
			aw.states[name] = state
		}

		if wantSession == sessionID {
			updateState()
		} else if !claimed[sessionID] {
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
				updateState()
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
// Logic: find the last meaningful entry (skip noise like progress, system,
// file-history-snapshot). Only idle when the last meaningful entry is an
// assistant message with stop_reason "end_turn". Everything else — streaming
// text, tool execution, tool results, user input — means something is
// happening, so report running.
func statusFromLines(lines []string) instance.Status {
	for i := len(lines) - 1; i >= 0; i-- {
		var entry activityEntry
		if err := json.Unmarshal([]byte(lines[i]), &entry); err != nil {
			continue
		}

		// Skip noise entries that don't indicate conversation state
		switch entry.Type {
		case "progress", "system", "file-history-snapshot":
			continue
		}

		// Only idle when Claude explicitly finished with end_turn
		if entry.Type == "assistant" && entry.Message != nil &&
			entry.Message.StopReason != nil && *entry.Message.StopReason == "end_turn" {
			return instance.StatusIdle
		}

		// Any other meaningful entry (assistant streaming, tool_use,
		// user tool_result, user text) means work is happening
		return instance.StatusRunning
	}

	return instance.StatusRunning
}

// activityEntry is the minimal JSONL structure we need for activity parsing.
type activityEntry struct {
	Type      string  `json:"type"`
	CostUSD   float64 `json:"costUSD"`
	Timestamp string  `json:"timestamp"`
	Message   *struct {
		Model   string `json:"model"`
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text,omitempty"`
			Name  string          `json:"name,omitempty"`
			Input json.RawMessage `json:"input,omitempty"`
		} `json:"content"`
		StopReason *string `json:"stop_reason"`
		Usage      *struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
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

// resolveJSONLPath finds the JSONL file for a given project dir and session ID.
func (aw *ActivityWatcher) resolveJSONLPath(projectDir, sessionID string) string {
	if sessionID != "" {
		return filepath.Join(projectDir, sessionID+".jsonl")
	}
	return latestJSONL(projectDir)
}

// readActivityState extracts activity kind, model, and text from the last lines of a JSONL file.
func readActivityState(path string) *ActivityState {
	state := &ActivityState{}
	lines := readLastNLines(path, 15)
	if len(lines) == 0 {
		return state
	}

	// Find last meaningful entry for kind + model
	for i := len(lines) - 1; i >= 0; i-- {
		var entry activityEntry
		if err := json.Unmarshal([]byte(lines[i]), &entry); err != nil {
			continue
		}
		switch entry.Type {
		case "progress", "system", "file-history-snapshot":
			continue
		}

		if state.Model == "" && entry.Message != nil && entry.Message.Model != "" {
			state.Model = shortenModel(entry.Message.Model)
		}

		if state.Kind == ActivityNone {
			state.Kind = activityKindFromEntry(&entry)
		}

		if state.Kind != ActivityNone && state.Model != "" {
			break
		}
	}

	// Activity text from last assistant entry
	for i := len(lines) - 1; i >= 0; i-- {
		if a := parseActivity(lines[i]); a != "" {
			state.Text = a
			break
		}
	}

	return state
}

// activityKindFromEntry determines the activity kind from a JSONL entry.
func activityKindFromEntry(entry *activityEntry) ActivityKind {
	if entry.Type == "assistant" && entry.Message != nil {
		// Check for end_turn first
		if entry.Message.StopReason != nil && *entry.Message.StopReason == "end_turn" {
			return ActivityWaiting
		}
		for _, c := range entry.Message.Content {
			if c.Type == "tool_use" {
				return toolToActivityKind(c.Name)
			}
		}
		return ActivityThinking
	}
	if entry.Type == "user" && entry.Message != nil {
		// User message with tool_result → kind of that tool
		for _, c := range entry.Message.Content {
			if c.Type == "tool_result" {
				// Can't determine tool from result alone; keep as Thinking
				return ActivityThinking
			}
		}
		return ActivityThinking
	}
	return ActivityNone
}

// scanMetricsFrom scans a JSONL file from the given byte offset, accumulating
// cost and token metrics into the state. Updates the stored offset.
func (aw *ActivityWatcher) scanMetricsFrom(path string, fromOffset int64, state *ActivityState) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return
	}

	if fromOffset >= stat.Size() {
		return
	}

	if _, err := f.Seek(fromOffset, 0); err != nil {
		return
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024) // up to 1MB lines
	for scanner.Scan() {
		line := scanner.Text()
		var entry activityEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		if entry.CostUSD > 0 {
			state.CostUSD += entry.CostUSD
		}
		if entry.Message != nil && entry.Message.Usage != nil {
			state.TokensIn += int64(entry.Message.Usage.InputTokens)
			state.TokensOut += int64(entry.Message.Usage.OutputTokens)
		}
		if entry.Timestamp != "" {
			if ts, err := time.Parse(time.RFC3339Nano, entry.Timestamp); err == nil {
				state.Timestamps = append(state.Timestamps, ts)
			}
		}
	}

	// Cap at 500 timestamps to bound memory
	if len(state.Timestamps) > 500 {
		state.Timestamps = state.Timestamps[len(state.Timestamps)-500:]
	}

	aw.offsets[path] = stat.Size()
}

// SparklineWindow is the time window for the activity sparkline.
const SparklineWindow = 30 * time.Minute

// sparkBlocks are Unicode block characters for sparkline rendering (9 levels).
var sparkBlocks = []rune{' ', '▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}

// RenderSparkline renders entry timestamps as a Unicode sparkline string.
func RenderSparkline(timestamps []time.Time, window time.Duration, width int) string {
	if width <= 0 {
		return ""
	}
	if len(timestamps) == 0 {
		return strings.Repeat(" ", width)
	}

	// Bucket timestamps into width slots
	buckets := make([]int, width)
	now := time.Now()
	cutoff := now.Add(-window)
	bucketDur := window / time.Duration(width)
	if bucketDur <= 0 {
		return strings.Repeat(" ", width)
	}

	for _, ts := range timestamps {
		if ts.Before(cutoff) || ts.After(now) {
			continue
		}
		idx := int(ts.Sub(cutoff) / bucketDur)
		if idx >= width {
			idx = width - 1
		}
		buckets[idx]++
	}

	maxVal := 0
	for _, v := range buckets {
		if v > maxVal {
			maxVal = v
		}
	}
	if maxVal == 0 {
		return strings.Repeat(" ", width)
	}

	var sb strings.Builder
	sb.Grow(width * 3)
	for _, v := range buckets {
		idx := v * 8 / maxVal
		if idx > 8 {
			idx = 8
		}
		sb.WriteRune(sparkBlocks[idx])
	}
	return sb.String()
}

// shortPath trims a file path to just the last 2 components.
func shortPath(p string) string {
	parts := strings.Split(p, "/")
	if len(parts) <= 2 {
		return p
	}
	return "…/" + strings.Join(parts[len(parts)-2:], "/")
}
