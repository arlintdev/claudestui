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
	TiledIDs map[string]bool // instance IDs in a tiled view — skip status refresh
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

	id := instance.GenerateID()
	windowID, err := l.tmux.NewWindow(id, opts.Dir, cmd)
	if err != nil {
		return nil, fmt.Errorf("launch %s: %w", opts.Name, err)
	}

	mode := instance.ModeSafe
	if opts.Dangerous {
		mode = instance.ModeDanger
	}

	inst := &instance.Instance{
		ID:        id,
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
	l.tmux.SetRemainOnExit(windowID)
	l.manager.Add(inst)
	if l.Activity != nil {
		l.Activity.Watch(inst.ID, inst.Dir, "")
	}
	return inst, nil
}

// Kill stops a Claude instance (kills tmux window) but keeps it in memory and DB
// so it can be resumed later. Captures the active session ID before killing.
func (l *Launcher) Kill(id string) error {
	inst := l.manager.Get(id)
	if inst == nil {
		return fmt.Errorf("instance %q not found", id)
	}
	// Capture session ID before killing so we can resume it later
	if sid := ActiveSessionID(inst.Dir); sid != "" {
		inst.SessionID = sid
	}
	_ = l.tmux.KillWindow(inst.WindowID)
	inst.Status = instance.StatusStopped
	inst.WindowID = ""
	l.manager.SaveInstance(id)
	return nil
}

// Delete permanently removes a Claude instance — kills tmux window, removes from
// memory and the database.
func (l *Launcher) Delete(id string) error {
	inst := l.manager.Get(id)
	if inst == nil {
		return fmt.Errorf("instance %q not found", id)
	}
	if inst.WindowID != "" {
		_ = l.tmux.KillWindow(inst.WindowID)
	}
	if l.Activity != nil {
		l.Activity.Unwatch(id)
	}
	l.manager.Delete(id)
	return nil
}

// Resume restarts a stopped instance. Uses the stored SessionID if available,
// otherwise falls back to plain --resume (most recent session).
func (l *Launcher) Resume(id string) error {
	inst := l.manager.Get(id)
	if inst == nil {
		return fmt.Errorf("instance %q not found", id)
	}

	// Kill old window — by ID if we have it, plus by name (=instance ID) to catch strays.
	_ = l.tmux.KillWindow(inst.WindowID)
	l.tmux.KillWindowByName(inst.ID)

	opts := LaunchOpts{
		Name:      inst.Name,
		Dir:       inst.Dir,
		Model:     inst.Model,
		Dangerous: inst.Mode == instance.ModeDanger,
		SessionID: inst.SessionID,
		Resume:    inst.SessionID == "", // plain --resume only if no specific session
	}
	cmd := BuildCommand(opts)
	windowID, err := l.tmux.NewWindow(inst.ID, opts.Dir, cmd)
	if err != nil {
		return fmt.Errorf("resume %s: %w", inst.Name, err)
	}

	inst.WindowID = windowID
	inst.Status = instance.StatusRunning
	inst.StartedAt = time.Now()
	l.tmux.SetWindowOption(windowID, "claudes-mode", inst.Mode.String())
	l.tmux.SetRemainOnExit(windowID)
	if l.Activity != nil {
		l.Activity.Watch(inst.ID, inst.Dir, inst.SessionID)
	}
	l.manager.SaveInstance(id)
	return nil
}

// ResumeSession resumes a specific Claude session by ID.
func (l *Launcher) ResumeSession(id, sessionID string) error {
	inst := l.manager.Get(id)
	if inst == nil {
		return fmt.Errorf("instance %q not found", id)
	}

	_ = l.tmux.KillWindow(inst.WindowID)
	l.tmux.KillWindowByName(inst.ID)

	opts := LaunchOpts{
		Name:      inst.Name,
		Dir:       inst.Dir,
		Dangerous: inst.Mode == instance.ModeDanger,
		SessionID: sessionID,
	}
	cmd := BuildCommand(opts)
	windowID, err := l.tmux.NewWindow(inst.ID, opts.Dir, cmd)
	if err != nil {
		return fmt.Errorf("resume session %s: %w", inst.Name, err)
	}

	inst.WindowID = windowID
	inst.SessionID = sessionID
	inst.Status = instance.StatusRunning
	inst.StartedAt = time.Now()
	l.tmux.SetWindowOption(windowID, "claudes-mode", inst.Mode.String())
	l.tmux.SetRemainOnExit(windowID)
	if l.Activity != nil {
		l.Activity.Watch(inst.ID, inst.Dir, sessionID)
	}
	l.manager.SaveInstance(id)
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
		if l.TiledIDs[inst.ID] {
			continue
		}
		prev := inst.Status
		w, found := windowMap[inst.ID]
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
		} else if w.PaneDead {
			// Window exists but the pane's process has exited (remain-on-exit kept it).
			if inst.Status != instance.StatusStopped {
				if sid := ActiveSessionID(inst.Dir); sid != "" {
					inst.SessionID = sid
				}
				inst.PanePID = ""
			}
			// Kill the stale window immediately so it doesn't linger.
			_ = l.tmux.KillWindow(w.ID)
			inst.WindowID = ""
			inst.Status = instance.StatusStopped
		} else {
			// Update PID from tmux
			inst.PanePID = w.PanePID
			if l.Activity != nil {
				inst.Status = l.Activity.Status(inst.ID)
			} else {
				inst.Status = StatusFromJSONL(inst.Dir, inst.SessionID)
			}
		}
		if inst.Status != prev {
			l.manager.SaveInstance(inst.ID)
		}
	}

	// Batch-update CPU/MEM for all live instances
	refreshProcStats(all)
}

// ToggleDangerous switches between safe and dangerous mode by killing the
// current session and resuming it with (or without) --dangerously-skip-permissions.
// Returns an error if the instance is currently busy (working).
func (l *Launcher) ToggleDangerous(id string) error {
	inst := l.manager.Get(id)
	if inst == nil {
		return fmt.Errorf("instance %q not found", id)
	}

	if inst.Status == instance.StatusRunning {
		return fmt.Errorf("instance %q is busy — wait until idle", inst.Name)
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
	windowID, err := l.tmux.NewWindow(inst.ID, opts.Dir, cmd)
	if err != nil {
		inst.Status = instance.StatusError
		return fmt.Errorf("relaunch %s: %w", inst.Name, err)
	}

	inst.WindowID = windowID
	inst.Mode = newMode
	inst.SessionID = sessionID
	inst.Status = instance.StatusRunning
	inst.StartedAt = time.Now()
	l.tmux.SetWindowOption(windowID, "claudes-mode", newMode.String())
	l.tmux.SetRemainOnExit(windowID)
	if l.Activity != nil {
		l.Activity.Watch(inst.ID, inst.Dir, sessionID)
	}
	l.manager.SaveInstance(id)
	return nil
}

// Attach switches the tmux client to the instance's window.
func (l *Launcher) Attach(id string) error {
	inst := l.manager.Get(id)
	if inst == nil {
		return fmt.Errorf("instance %q not found", id)
	}
	return l.tmux.SelectWindow(inst.WindowID)
}

// CaptureOutput grabs recent output from an instance.
func (l *Launcher) CaptureOutput(id string, lines int) (string, error) {
	inst := l.manager.Get(id)
	if inst == nil {
		return "", fmt.Errorf("instance %q not found", id)
	}
	return l.tmux.CapturePane(inst.WindowID, lines)
}

// Reconcile loads instances from the database, then merges with live tmux windows.
// Three cases: DB+window → running (update WindowID), DB+no-window → stopped,
// orphan window (not in DB) → add for backward compat.
func (l *Launcher) Reconcile() {
	// Step 1: Load persisted instances from the store.
	l.manager.LoadAll()

	// Step 2: Build a map of live tmux windows (keyed by window name = instance ID).
	windows, err := l.tmux.ListWindows()
	if err != nil {
		return
	}
	windowByName := make(map[string]tmux.WindowInfo)
	for _, w := range windows {
		if w.Name != tmux.DashboardWindowName && w.Name != "dashboard" {
			windowByName[w.Name] = w
		}
	}

	// Step 3: Reconcile DB instances with live windows.
	known := make(map[string]bool)
	for _, inst := range l.manager.All() {
		known[inst.ID] = true
		if w, found := windowByName[inst.ID]; found {
			// DB + window → running, update window ID.
			inst.WindowID = w.ID
			if w.PaneDead {
				inst.Status = instance.StatusStopped
			} else {
				inst.Status = instance.StatusRunning
			}
			l.manager.SaveInstance(inst.ID)
		} else {
			// DB + no window → stopped.
			inst.Status = instance.StatusStopped
			inst.WindowID = ""
		}
	}

	// Step 4: Orphan windows not in DB → add (backward compat).
	// For orphan windows, the window name might be a human name (old schema)
	// or an ID. Either way, generate a new ID and rename the window.
	for name, w := range windowByName {
		if known[name] {
			continue
		}

		status := instance.StatusRunning
		if w.PaneDead {
			status = instance.StatusStopped
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

		newID := instance.GenerateID()
		// Rename the tmux window from old name to new ID
		_ = l.tmux.RenameWindow(w.ID, newID)

		inst := &instance.Instance{
			ID:        newID,
			Name:      name, // preserve original name as display name
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
				l.Activity.Watch(inst.ID, inst.Dir, inst.SessionID)
			}
		}
	}
}
