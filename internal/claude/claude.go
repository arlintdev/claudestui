package claude

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

	// PreviewingID is the instance currently shown in the dashboard preview pane.
	// Its tmux window has been merged into the dashboard, so RefreshStatuses
	// must skip the window-based lookup for this instance.
	PreviewingID string
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
	Host      string // "" or "local" = local, "ssh:<hostname>", "docker:<image>"
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

// wrapCommand wraps a base command for remote execution based on host type.
// For docker, dir is mounted as /work inside the container.
func wrapCommand(baseCmd, host, dir, instID string) string {
	switch {
	case host == "" || host == "local":
		return baseCmd
	case strings.HasPrefix(host, "ssh:"):
		return fmt.Sprintf("ssh -t %s 'bash -lc %q'", host[4:], baseCmd)
	case strings.HasPrefix(host, "docker:"):
		image := host[7:]
		// Replace bare "claude" with "npx -y @anthropic-ai/claude-code" for
		// generic images (like node:20) that don't have claude pre-installed.
		dockerCmd := strings.Replace(baseCmd, "claude", "npx -y @anthropic-ai/claude-code", 1)
		name := "claudes-" + instID

		// Mount ~/.claude credentials into the container (read-only)
		var creds string
		if home, err := os.UserHomeDir(); err == nil {
			claudeDir := filepath.Join(home, ".claude")
			creds = fmt.Sprintf("-v %s:/root/.claude:ro", claudeDir)
		}

		var parts []string
		parts = append(parts, "docker run --rm -it --name", name)
		if creds != "" {
			parts = append(parts, creds)
		}
		if dir != "" {
			parts = append(parts, fmt.Sprintf("-v %s:/work -w /work", dir))
		}
		parts = append(parts, image, dockerCmd)
		return strings.Join(parts, " ")
	default:
		return baseCmd
	}
}

// Launch creates a new Claude instance in a tmux window.
func (l *Launcher) Launch(opts LaunchOpts) (*instance.Instance, error) {
	id := instance.GenerateID()
	cmd := wrapCommand(BuildCommand(opts), opts.Host, opts.Dir, id)

	// Remote instances don't use a local CWD for tmux
	dir := opts.Dir
	if opts.Host != "" && opts.Host != "local" {
		dir = ""
	}
	windowID, err := l.tmux.NewWindow(id, dir, cmd)
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
		Host:      opts.Host,
		Status:    instance.StatusRunning,
		Mode:      mode,
		WindowID:  windowID,
		StartedAt: time.Now(),
	}
	l.tmux.SetWindowOption(windowID, "claudes-mode", mode.String())
	l.tmux.SetRemainOnExit(windowID)
	l.tmux.SetPaneTitle(windowID, opts.Name)
	l.manager.Add(inst)
	if l.Activity != nil && !inst.IsRemote() {
		l.Activity.Watch(inst.ID, inst.Dir, "")
	}
	return inst, nil
}

// stopDockerContainer stops a docker container by instance ID.
func stopDockerContainer(instID string) {
	_ = exec.Command("docker", "stop", "claudes-"+instID).Run()
}

// removeDockerContainer force-removes a docker container by instance ID.
func removeDockerContainer(instID string) {
	_ = exec.Command("docker", "rm", "-f", "claudes-"+instID).Run()
}

// Kill stops a Claude instance (kills tmux window) but keeps it in memory and DB
// so it can be resumed later. Captures the active session ID before killing.
func (l *Launcher) Kill(id string) error {
	inst := l.manager.Get(id)
	if inst == nil {
		return fmt.Errorf("instance %q not found", id)
	}
	// Capture session ID before killing so we can resume it later (local only)
	if !inst.IsRemote() {
		if sid := ActiveSessionID(inst.Dir); sid != "" {
			inst.SessionID = sid
		}
	}
	// Stop docker container before killing tmux window
	if strings.HasPrefix(inst.Host, "docker:") {
		stopDockerContainer(id)
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
	// Force-remove docker container
	if strings.HasPrefix(inst.Host, "docker:") {
		removeDockerContainer(id)
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
		Resume:    true, // always use plain --resume so the user can pick the session
	}
	cmd := wrapCommand(BuildCommand(opts), inst.Host, inst.Dir, inst.ID)
	dir := opts.Dir
	if inst.IsRemote() {
		dir = ""
	}
	windowID, err := l.tmux.NewWindow(inst.ID, dir, cmd)
	if err != nil {
		return fmt.Errorf("resume %s: %w", inst.Name, err)
	}

	inst.WindowID = windowID
	inst.Status = instance.StatusRunning
	inst.StartedAt = time.Now()
	l.tmux.SetWindowOption(windowID, "claudes-mode", inst.Mode.String())
	l.tmux.SetRemainOnExit(windowID)
	l.tmux.SetPaneTitle(windowID, inst.Name)
	if l.Activity != nil && !inst.IsRemote() {
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
	cmd := wrapCommand(BuildCommand(opts), inst.Host, inst.Dir, inst.ID)
	dir := opts.Dir
	if inst.IsRemote() {
		dir = ""
	}
	windowID, err := l.tmux.NewWindow(inst.ID, dir, cmd)
	if err != nil {
		return fmt.Errorf("resume session %s: %w", inst.Name, err)
	}

	inst.WindowID = windowID
	inst.SessionID = sessionID
	inst.Status = instance.StatusRunning
	inst.StartedAt = time.Now()
	l.tmux.SetWindowOption(windowID, "claudes-mode", inst.Mode.String())
	l.tmux.SetRemainOnExit(windowID)
	l.tmux.SetPaneTitle(windowID, inst.Name)
	if l.Activity != nil && !inst.IsRemote() {
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
		prev := inst.Status

		// Instance is previewed inside the dashboard pane — its own window
		// was destroyed by join-pane, so skip the window-based check.
		if inst.ID == l.PreviewingID {
			// Still use JSONL activity status if available
			if l.Activity != nil && !inst.IsRemote() {
				inst.Status = l.Activity.Status(inst.ID)
			}
			if inst.Status != prev {
				l.manager.SaveInstance(inst.ID)
			}
			continue
		}

		w, found := windowMap[inst.ID]
		if !found {
			// Window is gone — instance is stopped
			if inst.Status != instance.StatusStopped {
				// Capture session ID on transition to stopped (local only)
				if !inst.IsRemote() {
					if sid := ActiveSessionID(inst.Dir); sid != "" {
						inst.SessionID = sid
					}
				}
				inst.WindowID = ""
				inst.PanePID = ""
			}
			inst.Status = instance.StatusStopped
		} else if w.PaneDead {
			// Window exists but the pane's process has exited (remain-on-exit kept it).
			if inst.Status != instance.StatusStopped {
				if !inst.IsRemote() {
					if sid := ActiveSessionID(inst.Dir); sid != "" {
						inst.SessionID = sid
					}
				}
				inst.PanePID = ""
			}
			// Kill the stale window immediately so it doesn't linger.
			_ = l.tmux.KillWindow(w.ID)
			inst.WindowID = ""
			inst.Status = instance.StatusStopped
		} else if inst.IsRemote() {
			// Remote instance with live pane — assume running (no JSONL access)
			inst.PanePID = w.PanePID
			inst.Status = instance.StatusRunning
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

	// Kill old window
	_ = l.tmux.KillWindow(inst.WindowID)

	// Relaunch with new mode — use plain --resume so the user can pick the session
	opts := LaunchOpts{
		Name:      inst.Name,
		Dir:       inst.Dir,
		Dangerous: newMode == instance.ModeDanger,
		Resume:    true,
	}
	cmd := wrapCommand(BuildCommand(opts), inst.Host, inst.Dir, inst.ID)
	dir := opts.Dir
	if inst.IsRemote() {
		dir = ""
	}
	windowID, err := l.tmux.NewWindow(inst.ID, dir, cmd)
	if err != nil {
		inst.Status = instance.StatusError
		return fmt.Errorf("relaunch %s: %w", inst.Name, err)
	}

	inst.WindowID = windowID
	inst.Mode = newMode
	inst.SessionID = ""
	inst.Status = instance.StatusRunning
	inst.StartedAt = time.Now()
	l.tmux.SetWindowOption(windowID, "claudes-mode", newMode.String())
	l.tmux.SetRemainOnExit(windowID)
	l.tmux.SetPaneTitle(windowID, inst.Name)
	if l.Activity != nil && !inst.IsRemote() {
		l.Activity.Watch(inst.ID, inst.Dir, "")
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

	// Step 5: Set up activity watches for all local instances with live windows.
	if l.Activity != nil {
		for _, inst := range l.manager.All() {
			if inst.WindowID != "" && !inst.IsRemote() {
				l.Activity.Watch(inst.ID, inst.Dir, inst.SessionID)
			}
		}
	}
}
