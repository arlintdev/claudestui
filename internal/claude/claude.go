package claude

import (
	"fmt"
	"strings"
	"time"

	"github.com/arlintdev/claudes/internal/instance"
	"github.com/arlintdev/claudes/internal/tmux"
)

// Launcher manages Claude Code instances via tmux.
type Launcher struct {
	tmux     *tmux.Client
	manager  *instance.Manager
	Activity *ActivityWatcher
	TiledNames map[string]bool // instances in a tiled view — skip status refresh
}

// NewLauncher creates a new Claude launcher.
func NewLauncher(tc *tmux.Client, mgr *instance.Manager) *Launcher {
	aw, _ := NewActivityWatcher() // nil-safe — Get/All return "" / empty map
	return &Launcher{tmux: tc, manager: mgr, Activity: aw}
}

// LaunchOpts configures a new Claude instance.
type LaunchOpts struct {
	Name      string
	Dir       string
	Task      string
	Model     string // claude model alias (e.g. "sonnet", "opus", "haiku")
	Dangerous bool
	Resume    bool
	SessionID string // specific session to resume
}

// BuildCommand constructs the claude CLI command string.
func BuildCommand(opts LaunchOpts) string {
	cmd := "claude"
	if opts.Model != "" {
		cmd += fmt.Sprintf(" --model %s", opts.Model)
	}
	if opts.Dangerous {
		cmd += " --dangerously-skip-permissions"
	}
	if opts.SessionID != "" {
		cmd += fmt.Sprintf(" --resume %s", opts.SessionID)
	} else if opts.Resume {
		cmd += " --resume"
	}
	if opts.Task != "" {
		cmd += fmt.Sprintf(" -p %q", opts.Task)
	}
	return cmd
}

// Launch creates a new Claude instance in a tmux window.
func (l *Launcher) Launch(opts LaunchOpts) (*instance.Instance, error) {
	cmd := BuildCommand(opts)

	windowID, err := l.tmux.NewWindow(opts.Name, opts.Dir, cmd)
	if err != nil {
		return nil, fmt.Errorf("launch %s: %w", opts.Name, err)
	}

	mode := instance.ModeSafe
	if opts.Dangerous {
		mode = instance.ModeDanger
	}

	inst := &instance.Instance{
		Name:      opts.Name,
		Dir:       opts.Dir,
		Task:      opts.Task,
		Model:     opts.Model,
		Status:    instance.StatusRunning,
		Mode:      mode,
		WindowID:  windowID,
		StartedAt: time.Now(),
	}
	l.tmux.SetWindowOption(windowID, "claudes-mode", mode.String())
	l.manager.Add(inst)
	if l.Activity != nil {
		l.Activity.Watch(inst.Name, inst.Dir, "")
	}
	return inst, nil
}

// Kill stops a Claude instance (kills tmux window) but keeps it in memory and DB
// so it can be resumed later. Captures the active session ID before killing.
func (l *Launcher) Kill(name string) error {
	inst := l.manager.Get(name)
	if inst == nil {
		return fmt.Errorf("instance %q not found", name)
	}
	// Capture session ID before killing so we can resume it later
	if sid := ActiveSessionID(inst.Dir); sid != "" {
		inst.SessionID = sid
	}
	_ = l.tmux.KillWindow(inst.WindowID)
	inst.Status = instance.StatusStopped
	inst.WindowID = ""
	l.manager.SaveInstance(name)
	return nil
}

// Delete permanently removes a Claude instance — kills tmux window, removes from
// memory and the database.
func (l *Launcher) Delete(name string) error {
	inst := l.manager.Get(name)
	if inst == nil {
		return fmt.Errorf("instance %q not found", name)
	}
	if inst.WindowID != "" {
		_ = l.tmux.KillWindow(inst.WindowID)
	}
	if l.Activity != nil {
		l.Activity.Unwatch(name)
	}
	l.manager.Delete(name)
	return nil
}

// Resume restarts a stopped instance. Uses the stored SessionID if available,
// otherwise falls back to plain --resume (most recent session).
func (l *Launcher) Resume(name string) error {
	inst := l.manager.Get(name)
	if inst == nil {
		return fmt.Errorf("instance %q not found", name)
	}

	// Kill old window if it exists
	_ = l.tmux.KillWindow(inst.WindowID)

	opts := LaunchOpts{
		Name:      inst.Name,
		Dir:       inst.Dir,
		Model:     inst.Model,
		Dangerous: inst.Mode == instance.ModeDanger,
		SessionID: inst.SessionID,
		Resume:    inst.SessionID == "", // plain --resume only if no specific session
	}
	cmd := BuildCommand(opts)
	windowID, err := l.tmux.NewWindow(opts.Name, opts.Dir, cmd)
	if err != nil {
		return fmt.Errorf("resume %s: %w", name, err)
	}

	inst.WindowID = windowID
	inst.Status = instance.StatusRunning
	inst.StartedAt = time.Now()
	l.tmux.SetWindowOption(windowID, "claudes-mode", inst.Mode.String())
	if l.Activity != nil {
		l.Activity.Watch(inst.Name, inst.Dir, inst.SessionID)
	}
	l.manager.SaveInstance(name)
	return nil
}

// ResumeSession resumes a specific Claude session by ID.
func (l *Launcher) ResumeSession(name, sessionID string) error {
	inst := l.manager.Get(name)
	if inst == nil {
		return fmt.Errorf("instance %q not found", name)
	}

	_ = l.tmux.KillWindow(inst.WindowID)

	opts := LaunchOpts{
		Name:      inst.Name,
		Dir:       inst.Dir,
		Dangerous: inst.Mode == instance.ModeDanger,
		SessionID: sessionID,
	}
	cmd := BuildCommand(opts)
	windowID, err := l.tmux.NewWindow(opts.Name, opts.Dir, cmd)
	if err != nil {
		return fmt.Errorf("resume session %s: %w", name, err)
	}

	inst.WindowID = windowID
	inst.SessionID = sessionID
	inst.Status = instance.StatusRunning
	inst.StartedAt = time.Now()
	l.tmux.SetWindowOption(windowID, "claudes-mode", inst.Mode.String())
	if l.Activity != nil {
		l.Activity.Watch(inst.Name, inst.Dir, sessionID)
	}
	l.manager.SaveInstance(name)
	return nil
}

// RefreshStatuses updates the status of all managed instances by checking
// tmux window state and JSONL session files. Persists changes when status transitions.
func (l *Launcher) RefreshStatuses() {
	windowMap := l.tmux.WindowInfoMap()

	all := l.manager.All()
	for _, inst := range all {
		// Skip instances that are in a tiled view — their windows
		// were merged via join-pane so they won't appear in ListWindows.
		if l.TiledNames[inst.Name] {
			continue
		}
		prev := inst.Status
		w, found := windowMap[inst.Name]
		if !found {
			// Window is gone — instance is stopped
			if inst.Status != instance.StatusStopped {
				// Capture session ID on transition to stopped
				if sid := ActiveSessionID(inst.Dir); sid != "" {
					inst.SessionID = sid
				}
				inst.WindowID = ""
				inst.PanePID = ""
			}
			inst.Status = instance.StatusStopped
		} else {
			// Update PID from tmux
			inst.PanePID = w.PanePID
			if l.Activity != nil {
				inst.Status = l.Activity.Status(inst.Name)
			} else {
				inst.Status = StatusFromJSONL(inst.Dir, inst.SessionID)
			}
		}
		if inst.Status != prev {
			l.manager.SaveInstance(inst.Name)
		}
	}

	// Batch-update CPU/MEM for all live instances
	refreshProcStats(all)
}

// ToggleDangerous switches between safe and dangerous mode by killing the
// current session and resuming it with (or without) --dangerously-skip-permissions.
// Returns an error if the instance is currently busy (working).
func (l *Launcher) ToggleDangerous(name string) error {
	inst := l.manager.Get(name)
	if inst == nil {
		return fmt.Errorf("instance %q not found", name)
	}

	if inst.Status == instance.StatusRunning {
		return fmt.Errorf("instance %q is busy — wait until idle", name)
	}

	// Determine new mode
	newMode := instance.ModeDanger
	if inst.Mode == instance.ModeDanger {
		newMode = instance.ModeSafe
	}

	// Find active session ID for resume
	sessionID := ActiveSessionID(inst.Dir)
	if sessionID == "" && inst.SessionID != "" {
		sessionID = inst.SessionID
	}

	// Kill old window
	_ = l.tmux.KillWindow(inst.WindowID)

	// Relaunch with new mode
	opts := LaunchOpts{
		Name:      inst.Name,
		Dir:       inst.Dir,
		Dangerous: newMode == instance.ModeDanger,
		SessionID: sessionID,
		Resume:    sessionID == "", // plain --resume if no specific session
	}
	cmd := BuildCommand(opts)
	windowID, err := l.tmux.NewWindow(opts.Name, opts.Dir, cmd)
	if err != nil {
		inst.Status = instance.StatusError
		return fmt.Errorf("relaunch %s: %w", name, err)
	}

	inst.WindowID = windowID
	inst.Mode = newMode
	inst.SessionID = sessionID
	inst.Status = instance.StatusRunning
	inst.StartedAt = time.Now()
	l.tmux.SetWindowOption(windowID, "claudes-mode", newMode.String())
	if l.Activity != nil {
		l.Activity.Watch(inst.Name, inst.Dir, sessionID)
	}
	l.manager.SaveInstance(name)
	return nil
}

// Attach switches the tmux client to the instance's window.
func (l *Launcher) Attach(name string) error {
	inst := l.manager.Get(name)
	if inst == nil {
		return fmt.Errorf("instance %q not found", name)
	}
	return l.tmux.SelectWindow(inst.WindowID)
}

// CaptureOutput grabs recent output from an instance.
func (l *Launcher) CaptureOutput(name string, lines int) (string, error) {
	inst := l.manager.Get(name)
	if inst == nil {
		return "", fmt.Errorf("instance %q not found", name)
	}
	return l.tmux.CapturePane(inst.WindowID, lines)
}

// Reconcile loads instances from the database, then merges with live tmux windows.
// Three cases: DB+window → running (update WindowID), DB+no-window → stopped,
// orphan window (not in DB) → add for backward compat.
func (l *Launcher) Reconcile() {
	// Step 1: Load persisted instances from the store.
	l.manager.LoadAll()

	// Step 2: Build a map of live tmux windows.
	windows, err := l.tmux.ListWindows()
	if err != nil {
		return
	}
	windowByName := make(map[string]tmux.WindowInfo)
	for _, w := range windows {
		if w.Name != "dashboard" {
			windowByName[w.Name] = w
		}
	}

	// Step 3: Reconcile DB instances with live windows.
	known := make(map[string]bool)
	for _, inst := range l.manager.All() {
		known[inst.Name] = true
		if w, found := windowByName[inst.Name]; found {
			// DB + window → running, update window ID.
			inst.WindowID = w.ID
			if w.PaneCommand == "claude" {
				inst.Status = instance.StatusRunning
			} else {
				inst.Status = instance.StatusStopped
			}
			l.manager.SaveInstance(inst.Name)
		} else {
			// DB + no window → stopped.
			inst.Status = instance.StatusStopped
			inst.WindowID = ""
		}
	}

	// Step 4: Orphan windows not in DB → add (backward compat).
	for name, w := range windowByName {
		if known[name] {
			continue
		}

		status := instance.StatusStopped
		if w.PaneCommand == "claude" {
			status = instance.StatusRunning
		}

		mode := instance.ModeSafe
		var sessionID string

		if modeOpt := l.tmux.GetWindowOption(w.ID, "claudes-mode"); modeOpt == "DANGER" {
			mode = instance.ModeDanger
		}

		fullCmd := l.tmux.GetFullCommand(w.PanePID)
		if mode == instance.ModeSafe && strings.Contains(fullCmd, "--dangerously-skip-permissions") {
			mode = instance.ModeDanger
		}
		if idx := strings.Index(fullCmd, "--resume "); idx != -1 {
			rest := fullCmd[idx+len("--resume "):]
			if parts := strings.Fields(rest); len(parts) > 0 {
				sessionID = parts[0]
			}
		}

		inst := &instance.Instance{
			Name:      name,
			Dir:       w.PanePath,
			Status:    status,
			Mode:      mode,
			WindowID:  w.ID,
			SessionID: sessionID,
			StartedAt: time.Now(),
		}
		l.manager.Add(inst) // Add persists to store automatically
	}

	// Step 5: Set up activity watches for all instances with live windows.
	if l.Activity != nil {
		for _, inst := range l.manager.All() {
			if inst.WindowID != "" {
				l.Activity.Watch(inst.Name, inst.Dir, inst.SessionID)
			}
		}
	}
}
