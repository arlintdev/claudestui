package tmux

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const SessionName = "claudes"
const DashboardWindowName = "_dashboard"

// Client wraps tmux CLI interactions.
type Client struct{}

// NewClient creates a new tmux client.
func NewClient() *Client {
	return &Client{}
}

// run executes a tmux command and returns trimmed stdout.
func (c *Client) run(args ...string) (string, error) {
	cmd := exec.Command("tmux", args...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// InsideClaudesSession returns true if the current process is running
// inside the "claudes" tmux session.
func (c *Client) InsideClaudesSession() bool {
	if os.Getenv("TMUX") == "" {
		return false
	}
	out, err := c.run("display-message", "-p", "#{session_name}")
	return err == nil && out == SessionName
}

// EnsureSession creates the claudes tmux session if it doesn't exist.
// If dashboardCmd is non-empty, the dashboard window runs that command.
// Returns true if a new session was created.
func (c *Client) EnsureSession(dashboardCmd string) (bool, error) {
	_, err := c.run("has-session", "-t", SessionName)
	if err == nil {
		return false, nil
	}
	args := []string{"new-session", "-d", "-s", SessionName, "-n", DashboardWindowName}
	if dashboardCmd != "" {
		args = append(args, dashboardCmd)
	}
	_, err = c.run(args...)
	if err != nil {
		return false, fmt.Errorf("create session: %w", err)
	}
	return true, nil
}

// RespawnDashboard kills whatever is running in the dashboard window
// and starts the given command in its place. Handles migration from
// the old "dashboard" name to the new "_dashboard" name.
func (c *Client) RespawnDashboard(cmd string) error {
	// Migrate: rename old "dashboard" window to "_dashboard" if needed.
	oldTarget := fmt.Sprintf("%s:dashboard", SessionName)
	c.run("rename-window", "-t", oldTarget, DashboardWindowName)

	target := fmt.Sprintf("%s:%s", SessionName, DashboardWindowName)
	_, err := c.run("respawn-window", "-k", "-t", target, cmd)
	if err != nil {
		return fmt.Errorf("respawn-window: %w", err)
	}
	return nil
}

// Attach replaces the current process with tmux attach-session.
// This never returns on success.
func (c *Client) Attach() error {
	tmuxBin, err := exec.LookPath("tmux")
	if err != nil {
		return fmt.Errorf("tmux not found: %w", err)
	}
	return execvp(tmuxBin, []string{"tmux", "attach-session", "-t", SessionName})
}

// SwitchClient switches the current tmux client to the claudes session.
// Use when already inside a different tmux session.
func (c *Client) SwitchClient() error {
	_, err := c.run("switch-client", "-t", SessionName)
	if err != nil {
		return fmt.Errorf("switch-client: %w", err)
	}
	return nil
}

// WindowInfo holds parsed tmux window data.
type WindowInfo struct {
	ID          string // e.g. "@1"
	Index       string // e.g. "1"
	Name        string
	Active      bool
	PaneCommand string // pane_current_command ("claude", "zsh", etc.)
	PanePath    string // pane_current_path (working directory)
	PanePID     string // pane_pid
	PaneDead    bool   // pane_dead — true when process exited (remain-on-exit)
}

// ListWindows returns all windows in the claudes session.
func (c *Client) ListWindows() ([]WindowInfo, error) {
	out, err := c.run("list-windows", "-t", SessionName,
		"-F", "#{window_id}\t#{window_index}\t#{window_name}\t#{window_active}\t#{pane_current_command}\t#{pane_current_path}\t#{pane_pid}\t#{pane_dead}")
	if err != nil {
		return nil, fmt.Errorf("list-windows: %w", err)
	}
	if out == "" {
		return nil, nil
	}

	var windows []WindowInfo
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(line, "\t", 8)
		if len(parts) < 8 {
			continue
		}
		windows = append(windows, WindowInfo{
			ID:          parts[0],
			Index:       parts[1],
			Name:        parts[2],
			Active:      parts[3] == "1",
			PaneCommand: parts[4],
			PanePath:    parts[5],
			PanePID:     parts[6],
			PaneDead:    parts[7] == "1",
		})
	}
	return windows, nil
}

// WindowInfoMap returns a map of window name → WindowInfo for quick lookup.
func (c *Client) WindowInfoMap() map[string]WindowInfo {
	windows, err := c.ListWindows()
	if err != nil {
		return nil
	}
	m := make(map[string]WindowInfo, len(windows))
	for _, w := range windows {
		m[w.Name] = w
	}
	return m
}

// SetWindowOption sets a user-defined option on a tmux window.
func (c *Client) SetWindowOption(windowID, key, value string) {
	target := fmt.Sprintf("%s:%s", SessionName, windowID)
	c.run("set-option", "-t", target, "@"+key, value)
}

// SetPaneTitle sets the pane title for a window (used in the status bar).
func (c *Client) SetPaneTitle(windowID, title string) {
	target := fmt.Sprintf("%s:%s", SessionName, windowID)
	c.run("select-pane", "-t", target, "-T", title)
}

// SetRemainOnExit enables remain-on-exit for a window so the pane stays
// visible (as dead) when its process exits, rather than vanishing instantly.
func (c *Client) SetRemainOnExit(windowID string) {
	target := fmt.Sprintf("%s:%s", SessionName, windowID)
	c.run("set-option", "-t", target, "remain-on-exit", "on")
}


// GetWindowOption reads a user-defined option from a tmux window.
func (c *Client) GetWindowOption(windowID, key string) string {
	target := fmt.Sprintf("%s:%s", SessionName, windowID)
	out, err := c.run("show-options", "-wv", "-t", target, "@"+key)
	if err != nil {
		return ""
	}
	return out
}

// GetFullCommand returns the full command line for a process by PID.
// Uses ps to retrieve the complete argv (needed to detect flags like --dangerously-skip-permissions).
func (c *Client) GetFullCommand(pid string) string {
	cmd := exec.Command("ps", "-p", pid, "-o", "args=")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// NewWindow creates a new window running the given command.
// The -d flag keeps the current window (dashboard) focused.
// KillWindowByName kills a window by name (best-effort). Used to clean up
// stale windows before creating a replacement.
func (c *Client) KillWindowByName(name string) {
	target := fmt.Sprintf("%s:%s", SessionName, name)
	c.run("kill-window", "-t", target)
}

func (c *Client) NewWindow(name, dir, shellCmd string) (string, error) {
	// -a: insert after current window, auto-picking a free index
	args := []string{"new-window", "-da", "-t", SessionName, "-n", name, "-P", "-F", "#{window_id}"}
	if dir != "" {
		args = append(args, "-c", dir)
	}
	if shellCmd != "" {
		args = append(args, shellCmd)
	}
	id, err := c.run(args...)
	if err != nil {
		return "", fmt.Errorf("new-window %s: %s (%w)", name, id, err)
	}
	return id, nil
}

// CapturePane captures the visible content of a window's pane.
func (c *Client) CapturePane(windowID string, lines int) (string, error) {
	target := fmt.Sprintf("%s:%s", SessionName, windowID)
	out, err := c.run("capture-pane", "-t", target, "-p",
		"-S", fmt.Sprintf("-%d", lines))
	if err != nil {
		return "", fmt.Errorf("capture-pane: %w", err)
	}
	return out, nil
}

// JoinPaneRight joins a window's pane into another window as a right-side
// horizontal split. Returns the pane ID of the joined pane. The source window
// is destroyed by tmux.
func (c *Client) JoinPaneRight(srcWindowID, dstWindowID string, percent int) (string, error) {
	src := fmt.Sprintf("%s:%s", SessionName, srcWindowID)
	dst := fmt.Sprintf("%s:%s", SessionName, dstWindowID)
	out, err := c.run("join-pane", "-h", "-s", src, "-t", dst,
		"-l", fmt.Sprintf("%d%%", percent))
	if err != nil {
		return "", fmt.Errorf("join-pane %s → %s: %s (%w)", src, dst, out, err)
	}
	// Find the pane ID of the joined pane (last/rightmost in the target window)
	panes, err := c.ListPanes(dstWindowID)
	if err != nil {
		return "", err
	}
	if len(panes) < 2 {
		return "", fmt.Errorf("join-pane: expected 2+ panes, got %d", len(panes))
	}
	return panes[len(panes)-1].PaneID, nil
}

// FocusPane focuses a specific pane by its ID (e.g. "%5").
func (c *Client) FocusPane(paneID string) error {
	_, err := c.run("select-pane", "-t", paneID)
	if err != nil {
		return fmt.Errorf("select-pane: %w", err)
	}
	return nil
}

// FocusDashboardPane focuses the first (leftmost) pane in the dashboard window.
func (c *Client) FocusDashboardPane() error {
	target := fmt.Sprintf("%s:%s.0", SessionName, DashboardWindowName)
	_, err := c.run("select-pane", "-t", target)
	if err != nil {
		return fmt.Errorf("select-pane: %w", err)
	}
	return nil
}

// SwapPane swaps a pane (by pane ID) with the first pane of a target window.
// Both the source and target panes continue to exist, just in each other's location.
func (c *Client) SwapPane(srcPaneID, dstWindowID string) error {
	dst := fmt.Sprintf("%s:%s", SessionName, dstWindowID)
	out, err := c.run("swap-pane", "-d", "-s", srcPaneID, "-t", dst)
	if err != nil {
		return fmt.Errorf("swap-pane %s → %s: %s (%w)", srcPaneID, dst, out, err)
	}
	return nil
}

// KillWindow destroys a window by ID. No-op if windowID is empty.
func (c *Client) KillWindow(windowID string) error {
	if windowID == "" {
		return nil
	}
	target := fmt.Sprintf("%s:%s", SessionName, windowID)
	_, err := c.run("kill-window", "-t", target)
	if err != nil {
		return fmt.Errorf("kill-window: %w", err)
	}
	return nil
}

// SendKeys sends keystrokes to a window's pane.
func (c *Client) SendKeys(windowID string, keys ...string) error {
	target := fmt.Sprintf("%s:%s", SessionName, windowID)
	args := append([]string{"send-keys", "-t", target}, keys...)
	_, err := c.run(args...)
	if err != nil {
		return fmt.Errorf("send-keys: %w", err)
	}
	return nil
}

// SelectWindow switches the active window in the session. No-op if windowID is empty.
func (c *Client) SelectWindow(windowID string) error {
	if windowID == "" {
		return nil
	}
	target := fmt.Sprintf("%s:%s", SessionName, windowID)
	_, err := c.run("select-window", "-t", target)
	if err != nil {
		return fmt.Errorf("select-window: %w", err)
	}
	return nil
}

// SetupKeybindings registers claudes-specific tmux keybindings in the session.
// Ctrl-Space (root table, no prefix needed) jumps back to the dashboard.
// Ctrl-Left/Right cycle through windows.
func (c *Client) SetupKeybindings() {
	dashTarget := fmt.Sprintf("%s:%s", SessionName, DashboardWindowName)
	dashPane := fmt.Sprintf("%s:%s.0", SessionName, DashboardWindowName)
	// Ctrl-Space: jump to dashboard window AND focus the first (dashboard) pane.
	// Uses run-shell to chain commands since exec.Command can't pass ";" as a tmux separator.
	c.run("bind-key", "-T", "root", "C-Space",
		"run-shell", fmt.Sprintf("tmux select-window -t %s && tmux select-pane -t %s", dashTarget, dashPane))
	// Vim-style window switching
	c.run("bind-key", "-T", "root", "C-h", "previous-window")
	c.run("bind-key", "-T", "root", "C-l", "next-window")
	// Pane navigation for tiled views
	c.run("bind-key", "-T", "root", "C-j", "select-pane", "-D")
	c.run("bind-key", "-T", "root", "C-k", "select-pane", "-U")

	// When a pane dies, switch back to the dashboard and kill the dead window.
	dashCmd := fmt.Sprintf("tmux select-window -t %s; tmux kill-pane -t '#{pane_id}'", dashTarget)
	c.run("set-hook", "-t", SessionName, "pane-died", "run-shell", dashCmd)

	// Mouse on for scroll; hold Shift to select/copy with native terminal selection.
	c.run("set-option", "-t", SessionName, "mouse", "on")
	// Prevent apps (like Claude Code) from overwriting pane/window titles via escape sequences.
	c.run("set-option", "-t", SessionName, "allow-rename", "off")

	// Pane borders: cyan for active, dark gray for inactive
	c.run("set-option", "-t", SessionName, "pane-border-style", "fg=#333333")
	c.run("set-option", "-t", SessionName, "pane-active-border-style", "fg=#DA7756")
	c.run("set-option", "-t", SessionName, "pane-border-lines", "heavy")

	// Status bar
	c.run("set-option", "-t", SessionName, "status", "on")
	c.run("set-option", "-t", SessionName, "status-style", "bg=default,fg=#666666")
	c.run("set-option", "-t", SessionName, "status-left", "")
	c.run("set-option", "-t", SessionName, "status-right",
		"#[fg=#DA7756]#{pane_title}#[fg=#666666]  │  ^Space dashboard  ^h/^l windows ")
	c.run("set-option", "-t", SessionName, "status-right-length", "80")

	// Window list: subtle for inactive, cyan highlight for active
	c.run("set-option", "-t", SessionName, "window-status-format", "#[fg=#666666] #W ")
	c.run("set-option", "-t", SessionName, "window-status-current-format", "#[fg=#DA7756,bold] #W ")
	c.run("set-option", "-t", SessionName, "window-status-separator", "#[fg=#333333]│")
}

// CleanupKeybindings removes claudes-specific tmux keybindings.
func (c *Client) CleanupKeybindings() {
	c.run("unbind-key", "-T", "root", "C-Space")
	c.run("unbind-key", "-T", "root", "C-h")
	c.run("unbind-key", "-T", "root", "C-l")
	c.run("unbind-key", "-T", "root", "C-j")
	c.run("unbind-key", "-T", "root", "C-k")
	c.run("set-hook", "-u", "-t", SessionName, "pane-died")
}

// DetachClient detaches the current tmux client, returning the user
// to their terminal (if they attached) or previous session (if they switched).
func (c *Client) DetachClient() error {
	_, err := c.run("detach-client")
	if err != nil {
		return fmt.Errorf("detach-client: %w", err)
	}
	return nil
}

// KeepDashboardAlive sets remain-on-exit on the dashboard window so the
// pane persists after the TUI process exits. This prevents the session
// from being destroyed when instances are still running.
func (c *Client) KeepDashboardAlive() {
	// Migrate old name if needed
	oldTarget := fmt.Sprintf("%s:dashboard", SessionName)
	c.run("rename-window", "-t", oldTarget, DashboardWindowName)

	target := fmt.Sprintf("%s:%s", SessionName, DashboardWindowName)
	c.run("set-option", "-t", target, "remain-on-exit", "on")
}

// HasSession checks if the claudes session exists.
func (c *Client) HasSession() bool {
	_, err := c.run("has-session", "-t", SessionName)
	return err == nil
}

// RenameWindow renames a window.
func (c *Client) RenameWindow(windowID, name string) error {
	target := fmt.Sprintf("%s:%s", SessionName, windowID)
	_, err := c.run("rename-window", "-t", target, name)
	if err != nil {
		return fmt.Errorf("rename-window: %w", err)
	}
	return nil
}

// PaneInfo holds parsed tmux pane data.
type PaneInfo struct {
	PaneID string // e.g. "%5"
	PID    string // pane_pid
}

// JoinPane moves a pane from srcWindowID into dstWindowID (destroys src window).
func (c *Client) JoinPane(srcWindowID, dstWindowID string) error {
	src := fmt.Sprintf("%s:%s", SessionName, srcWindowID)
	dst := fmt.Sprintf("%s:%s", SessionName, dstWindowID)
	_, err := c.run("join-pane", "-s", src, "-t", dst)
	if err != nil {
		return fmt.Errorf("join-pane: %w", err)
	}
	return nil
}

// BreakPane breaks a pane out of its window into a new standalone window.
// Returns the new window ID.
func (c *Client) BreakPane(paneID, windowName string) (string, error) {
	id, err := c.run("break-pane", "-d", "-s", paneID, "-n", windowName, "-P", "-F", "#{window_id}")
	if err != nil {
		return "", fmt.Errorf("break-pane: %w", err)
	}
	return id, nil
}

// SelectLayoutTiled applies the "tiled" layout to a window.
func (c *Client) SelectLayoutTiled(windowID string) error {
	target := fmt.Sprintf("%s:%s", SessionName, windowID)
	_, err := c.run("select-layout", "-t", target, "tiled")
	if err != nil {
		return fmt.Errorf("select-layout: %w", err)
	}
	return nil
}

// ListPanes returns all panes in a window.
func (c *Client) ListPanes(windowID string) ([]PaneInfo, error) {
	target := fmt.Sprintf("%s:%s", SessionName, windowID)
	out, err := c.run("list-panes", "-t", target, "-F", "#{pane_id}\t#{pane_pid}")
	if err != nil {
		return nil, fmt.Errorf("list-panes: %w", err)
	}
	if out == "" {
		return nil, nil
	}
	var panes []PaneInfo
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) < 2 {
			continue
		}
		panes = append(panes, PaneInfo{PaneID: parts[0], PID: parts[1]})
	}
	return panes, nil
}
