package claude

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/arlintdev/claudes/internal/daemon"
	"github.com/arlintdev/claudes/internal/instance"
)

// Launcher manages Claude Code instances via the daemon.
type Launcher struct {
	daemon   *daemon.Client
	manager  *instance.Manager
	Activity *ActivityWatcher
}

// NewLauncher creates a new Claude launcher.
func NewLauncher(dc *daemon.Client, mgr *instance.Manager) *Launcher {
	aw, _ := NewActivityWatcher() // nil-safe — Get/All return "" / empty map
	return &Launcher{daemon: dc, manager: mgr, Activity: aw}
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
	Cols      int    // terminal width
	Rows      int    // terminal height
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
func wrapCommand(baseCmd, host, dir, instID string) string {
	switch {
	case host == "" || host == "local":
		return baseCmd
	case strings.HasPrefix(host, "ssh:"):
		return fmt.Sprintf("ssh -t %s 'bash -lc %q'", host[4:], baseCmd)
	case strings.HasPrefix(host, "docker:"):
		image := host[7:]
		dockerCmd := strings.Replace(baseCmd, "claude", "npx -y @anthropic-ai/claude-code", 1)
		name := "claudes-" + instID

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

// Launch creates a new Claude instance via the daemon.
func (l *Launcher) Launch(opts LaunchOpts) (*instance.Instance, error) {
	id := instance.GenerateID()
	cmd := wrapCommand(BuildCommand(opts), opts.Host, opts.Dir, id)

	dir := opts.Dir
	if opts.Host != "" && opts.Host != "local" {
		dir = ""
	}
	if dir == "" {
		dir, _ = os.UserHomeDir()
	}

	cols, rows := opts.Cols, opts.Rows
	if cols <= 0 {
		cols = 120
	}
	if rows <= 0 {
		rows = 40
	}

	pid, err := l.daemon.Launch(daemon.LaunchParams{
		ID:      id,
		Command: cmd,
		Dir:     dir,
		Cols:    cols,
		Rows:    rows,
	})
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
		DaemonPID: pid,
		StartedAt: time.Now(),
	}
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

// Kill stops a Claude instance but keeps it in memory and DB for resume.
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
	// Stop docker container
	if strings.HasPrefix(inst.Host, "docker:") {
		stopDockerContainer(id)
	}
	_ = l.daemon.Kill(id)
	inst.Status = instance.StatusStopped
	inst.DaemonPID = 0
	l.manager.SaveInstance(id)
	return nil
}

// Delete permanently removes a Claude instance.
func (l *Launcher) Delete(id string) error {
	inst := l.manager.Get(id)
	if inst == nil {
		return fmt.Errorf("instance %q not found", id)
	}
	if strings.HasPrefix(inst.Host, "docker:") {
		removeDockerContainer(id)
	}
	if inst.DaemonPID != 0 {
		_ = l.daemon.Kill(id)
	}
	if l.Activity != nil {
		l.Activity.Unwatch(id)
	}
	l.manager.Delete(id)
	return nil
}

// Resume restarts a stopped instance.
func (l *Launcher) Resume(id string, cols, rows int) error {
	inst := l.manager.Get(id)
	if inst == nil {
		return fmt.Errorf("instance %q not found", id)
	}

	// Kill existing daemon process if any
	_ = l.daemon.Kill(id)

	opts := LaunchOpts{
		Name:      inst.Name,
		Dir:       inst.Dir,
		Model:     inst.Model,
		Dangerous: inst.Mode == instance.ModeDanger,
		Resume:    inst.SessionID == "",
		SessionID: inst.SessionID,
		Cols:      cols,
		Rows:      rows,
	}
	cmd := wrapCommand(BuildCommand(opts), inst.Host, inst.Dir, inst.ID)
	dir := opts.Dir
	if inst.IsRemote() {
		dir = ""
	}
	if dir == "" {
		dir, _ = os.UserHomeDir()
	}

	if cols <= 0 {
		cols = 120
	}
	if rows <= 0 {
		rows = 40
	}

	pid, err := l.daemon.Launch(daemon.LaunchParams{
		ID:      inst.ID,
		Command: cmd,
		Dir:     dir,
		Cols:    cols,
		Rows:    rows,
	})
	if err != nil {
		return fmt.Errorf("resume %s: %w", inst.Name, err)
	}

	inst.DaemonPID = pid
	inst.Status = instance.StatusRunning
	inst.StartedAt = time.Now()
	if l.Activity != nil && !inst.IsRemote() {
		l.Activity.Watch(inst.ID, inst.Dir, inst.SessionID)
	}
	l.manager.SaveInstance(id)
	return nil
}

// ResumeSession resumes a specific Claude session by ID.
func (l *Launcher) ResumeSession(id, sessionID string, cols, rows int) error {
	inst := l.manager.Get(id)
	if inst == nil {
		return fmt.Errorf("instance %q not found", id)
	}

	_ = l.daemon.Kill(id)

	opts := LaunchOpts{
		Name:      inst.Name,
		Dir:       inst.Dir,
		Dangerous: inst.Mode == instance.ModeDanger,
		SessionID: sessionID,
		Cols:      cols,
		Rows:      rows,
	}
	cmd := wrapCommand(BuildCommand(opts), inst.Host, inst.Dir, inst.ID)
	dir := opts.Dir
	if inst.IsRemote() {
		dir = ""
	}
	if dir == "" {
		dir, _ = os.UserHomeDir()
	}

	if cols <= 0 {
		cols = 120
	}
	if rows <= 0 {
		rows = 40
	}

	pid, err := l.daemon.Launch(daemon.LaunchParams{
		ID:      inst.ID,
		Command: cmd,
		Dir:     dir,
		Cols:    cols,
		Rows:    rows,
	})
	if err != nil {
		return fmt.Errorf("resume session %s: %w", inst.Name, err)
	}

	inst.DaemonPID = pid
	inst.SessionID = sessionID
	inst.Status = instance.StatusRunning
	inst.StartedAt = time.Now()
	if l.Activity != nil && !inst.IsRemote() {
		l.Activity.Watch(inst.ID, inst.Dir, sessionID)
	}
	l.manager.SaveInstance(id)
	return nil
}

// RefreshStatuses updates the status of all managed instances by checking
// daemon process state and JSONL session files.
func (l *Launcher) RefreshStatuses() {
	procs, err := l.daemon.List()
	if err != nil {
		return
	}
	procMap := make(map[string]daemon.ProcessInfo)
	for _, p := range procs {
		procMap[p.ID] = p
	}

	all := l.manager.All()
	for _, inst := range all {
		prev := inst.Status

		p, found := procMap[inst.ID]
		if !found || !p.Running {
			// Process gone or exited
			if inst.Status != instance.StatusStopped {
				if !inst.IsRemote() {
					if sid := ActiveSessionID(inst.Dir); sid != "" {
						inst.SessionID = sid
					}
				}
				inst.DaemonPID = 0
			}
			inst.Status = instance.StatusStopped
		} else if inst.IsRemote() {
			inst.DaemonPID = p.PID
			inst.Status = instance.StatusRunning
		} else {
			inst.DaemonPID = p.PID
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

// ToggleDangerous switches between safe and dangerous mode.
func (l *Launcher) ToggleDangerous(id string, cols, rows int) error {
	inst := l.manager.Get(id)
	if inst == nil {
		return fmt.Errorf("instance %q not found", id)
	}

	if inst.Status == instance.StatusRunning {
		return fmt.Errorf("instance %q is busy — wait until idle", inst.Name)
	}

	newMode := instance.ModeDanger
	if inst.Mode == instance.ModeDanger {
		newMode = instance.ModeSafe
	}

	_ = l.daemon.Kill(id)

	opts := LaunchOpts{
		Name:      inst.Name,
		Dir:       inst.Dir,
		Dangerous: newMode == instance.ModeDanger,
		Resume:    true,
		Cols:      cols,
		Rows:      rows,
	}
	cmd := wrapCommand(BuildCommand(opts), inst.Host, inst.Dir, inst.ID)
	dir := opts.Dir
	if inst.IsRemote() {
		dir = ""
	}
	if dir == "" {
		dir, _ = os.UserHomeDir()
	}
	if cols <= 0 {
		cols = 120
	}
	if rows <= 0 {
		rows = 40
	}

	pid, err := l.daemon.Launch(daemon.LaunchParams{
		ID:      inst.ID,
		Command: cmd,
		Dir:     dir,
		Cols:    cols,
		Rows:    rows,
	})
	if err != nil {
		inst.Status = instance.StatusError
		return fmt.Errorf("relaunch %s: %w", inst.Name, err)
	}

	inst.DaemonPID = pid
	inst.Mode = newMode
	inst.SessionID = ""
	inst.Status = instance.StatusRunning
	inst.StartedAt = time.Now()
	if l.Activity != nil && !inst.IsRemote() {
		l.Activity.Watch(inst.ID, inst.Dir, "")
	}
	l.manager.SaveInstance(id)
	return nil
}

// Reconcile loads instances from the database, then merges with live daemon processes.
func (l *Launcher) Reconcile() {
	l.manager.LoadAll()

	procs, err := l.daemon.List()
	if err != nil {
		return
	}
	procMap := make(map[string]daemon.ProcessInfo)
	for _, p := range procs {
		procMap[p.ID] = p
	}

	for _, inst := range l.manager.All() {
		if p, found := procMap[inst.ID]; found {
			inst.DaemonPID = p.PID
			if p.Running {
				inst.Status = instance.StatusRunning
			} else {
				inst.Status = instance.StatusStopped
			}
			l.manager.SaveInstance(inst.ID)
		} else {
			inst.Status = instance.StatusStopped
			inst.DaemonPID = 0
		}
	}

	// Set up activity watches for running local instances
	if l.Activity != nil {
		for _, inst := range l.manager.All() {
			if inst.DaemonPID != 0 && !inst.IsRemote() {
				l.Activity.Watch(inst.ID, inst.Dir, inst.SessionID)
			}
		}
	}
}
