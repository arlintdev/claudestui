package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/arlintdev/claudes/internal/claude"
	"github.com/arlintdev/claudes/internal/config"
	"github.com/arlintdev/claudes/internal/instance"
	"github.com/arlintdev/claudes/internal/tmux"
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// baseHeaderLines is the fixed portion: logo/info (3 lines) + tagline (1 line) + resource bar (1 line).
// The shortcut bar adds 1+ lines depending on terminal width.
const baseHeaderLines = 5

// Messages
type tickMsg time.Time
type statusMsg struct{}
type errMsg struct{ err error }

// tiledState tracks an active tiled view so we can break it apart later.
type tiledState struct {
	baseWindowID  string            // the window all panes were joined into
	paneNames     []string          // instance names in the tiled view
	originalWindows map[string]string // name → original windowID (before join)
}

// Model is the root Bubble Tea model.
type Model struct {
	keys     KeyMap
	theme    Theme
	cfg      config.Config
	list   ListView
	dialog Dialog
	help     HelpOverlay
	manager  *instance.Manager
	launcher *claude.Launcher
	tmux     *tmux.Client
	sessions *claude.SessionStore

	width        int
	height       int
	shortcutRows int // number of lines the shortcut bar occupies
	err          error
	errAt        time.Time // when error was set, for flash bar timeout
	tiled        *tiledState // active tiled view, if any
}

// New creates the root model.
func New(cfg config.Config, mgr *instance.Manager, tc *tmux.Client, launcher *claude.Launcher, sessions *claude.SessionStore) Model {
	theme := DefaultTheme()
	return Model{
		keys:   DefaultKeyMap(),
		theme:  theme,
		cfg:    cfg,
		list:   NewListView(theme),
		dialog: NewDialog(theme),
		help:     NewHelpOverlay(theme),
		manager:  mgr,
		launcher: launcher,
		tmux:     tc,
		sessions: sessions,
	}
}

// shortcutLabels are the key/desc pairs for the shortcut bar.
var shortcutLabels = []struct{ key, desc string }{
	{"n", "New"}, {"d", "Danger"}, {"^s", "Stop"}, {"^x", "Stop Idle"}, {"^d", "Delete"},
	{"Enter", "Attach"}, {"^r", "Resume"}, {"Space", "Select"}, {"^a", "All"},
	{"^g", "Group"}, {"^b", "Ungroup"}, {"/", "Filter"}, {"L", "Profile"}, {"^hjkl", "Panes"}, {"^Space", "Back"}, {"?", "Help"}, {"q", "Quit"},
}

// calcShortcutRows computes how many lines the shortcut bar needs at current width.
func (m Model) calcShortcutRows() int {
	if m.width <= 0 {
		return 1
	}
	rows := 1
	curW := 1 // leading space
	for i, s := range shortcutLabels {
		// Approximate visible width: <key> desc + separator
		partW := lipgloss.Width(m.theme.ShortcutKey.Render("<"+s.key+">")) +
			lipgloss.Width(m.theme.ShortcutDesc.Render(" "+s.desc))
		addW := partW
		if i > 0 && curW > 1 {
			addW += 2 // separator "  "
		}
		if curW+addW > m.width && curW > 1 {
			rows++
			curW = 1 + partW
		} else {
			if i > 0 && curW > 1 {
				curW += 2
			}
			curW += partW
		}
	}
	return rows
}

// headerHeight returns the total header lines (info + shortcuts + resource bar).
func (m Model) headerHeight() int {
	rows := m.shortcutRows
	if rows < 1 {
		rows = 1
	}
	return baseHeaderLines + rows
}

// Init starts the tick timer.
func (m Model) Init() tea.Cmd {
	return m.tick()
}

func (m Model) tick() tea.Cmd {
	return tea.Tick(time.Duration(m.cfg.PollInterval)*time.Millisecond, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// Update handles all messages.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.layout()
		m.launcher.RefreshStatuses()
		m.list.Sync(m.manager.All())
		return m, nil

	case tickMsg:
		m.launcher.RefreshStatuses()
		m.list.Sync(m.manager.All())

		// Clear error flash after 5 seconds
		if m.err != nil && time.Since(m.errAt) > 5*time.Second {
			m.err = nil
		}

		// Trigger session scan (rate-limited internally)
		if m.sessions != nil {
			go m.sessions.Scan()
		}

		// Poll system stats (rate-limited internally)
		go pollSysInfo()

		// Sync per-instance activity from JSONL watcher
		if m.launcher.Activity != nil {
			for name, line := range m.launcher.Activity.All() {
				m.list.SetActivity(name, line)
			}
		}

		return m, m.tick()

	case errMsg:
		m.err = msg.err
		m.errAt = time.Now()
		return m, nil

	case tea.MouseMsg:
		return m.handleMouse(msg)

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// If a dialog is open, route keys there
	if m.dialog.Kind != DialogNone {
		return m.handleDialogKey(msg)
	}

	// Help overlay intercepts
	if m.help.Visible() {
		if key.Matches(msg, m.keys.Help) || msg.String() == "esc" {
			m.help.Hide()
		}
		return m, nil
	}

	switch {
	case key.Matches(msg, m.keys.Quit):
		if m.manager.Count() > 0 {
			// Instances still running — keep the dashboard pane alive
			// so the session isn't destroyed, then detach the client.
			m.tmux.KeepDashboardAlive()
			_ = m.tmux.DetachClient()
		}
		return m, tea.Quit

	case key.Matches(msg, m.keys.Help):
		m.help.Toggle()

	case key.Matches(msg, m.keys.Up):
		m.list.MoveUp()

	case key.Matches(msg, m.keys.Down):
		m.list.MoveDown()

	case key.Matches(msg, m.keys.New):
		m.dialog.OpenNew()
		m.layout()

	case msg.String() == " ":
		m.list.ToggleSelected()

	case key.Matches(msg, m.keys.SelectAll):
		m.list.SelectAll()

	case key.Matches(msg, m.keys.Group):
		if names := m.list.SelectedNames(); len(names) >= 2 {
			m.manager.SetGroup(names, "group-"+names[0])
			m.list.ClearSelected()
		}

	case key.Matches(msg, m.keys.BreakTile):
		if m.tiled != nil {
			m.breakTiledView()
		} else if names := m.list.SelectedNames(); names != nil {
			m.manager.ClearGroup(names)
			m.list.ClearSelected()
		} else if sel := m.list.Selected(); sel != nil && sel.GroupName != "" {
			m.manager.ClearGroup([]string{sel.Name})
		}

	case key.Matches(msg, m.keys.Stop):
		if names := m.list.SelectedNames(); names != nil {
			// Batch stop
			for _, name := range names {
				inst := m.manager.Get(name)
				if inst != nil && inst.Status != instance.StatusStopped {
					if err := m.launcher.Kill(name); err != nil {
						m.err = err
						m.errAt = time.Now()
					}
				}
			}
			m.list.ClearSelected()
		} else if sel := m.list.Selected(); sel != nil && sel.Status != instance.StatusStopped {
			if err := m.launcher.Kill(sel.Name); err != nil {
				m.err = err
				m.errAt = time.Now()
			}
		}

	case key.Matches(msg, m.keys.StopIdle):
		for _, inst := range m.manager.All() {
			if inst.Status == instance.StatusIdle {
				if err := m.launcher.Kill(inst.Name); err != nil {
					m.err = err
					m.errAt = time.Now()
				}
			}
		}

	case key.Matches(msg, m.keys.Delete):
		if names := m.list.SelectedNames(); names != nil {
			m.dialog.OpenConfirmBatchDelete(names)
		} else if sel := m.list.Selected(); sel != nil {
			m.dialog.OpenConfirmDelete(sel.Name)
		}

	case key.Matches(msg, m.keys.Danger):
		if sel := m.list.Selected(); sel != nil {
			if err := m.launcher.ToggleDangerous(sel.Name); err != nil {
				m.err = err
				m.errAt = time.Now()
			}
		}

	case key.Matches(msg, m.keys.Resume):
		if sel := m.list.Selected(); sel != nil {
			if sel.Status == instance.StatusStopped || sel.Status == instance.StatusError {
				if err := m.launcher.Resume(sel.Name); err != nil {
					m.err = err
					m.errAt = time.Now()
				}
			}
		}

	case key.Matches(msg, m.keys.Attach):
		if names := m.list.SelectedNames(); names != nil {
			m.openTiledView(names)
			m.list.ClearSelected()
		} else if sel := m.list.Selected(); sel != nil {
			// If instance is in a group, open all group members tiled
			if sel.GroupName != "" {
				members := m.manager.GroupMembers(sel.GroupName)
				if len(members) > 1 {
					m.openTiledView(members)
					return m, nil
				}
			}
			// Running/idle: just attach. Stopped: resume + attach.
			if sel.Status == instance.StatusStopped || sel.Status == instance.StatusError {
				if err := m.launcher.Resume(sel.Name); err != nil {
					m.err = err
					m.errAt = time.Now()
					return m, nil
				}
				// Re-fetch to get updated WindowID after resume
				sel = m.manager.Get(sel.Name)
			}
			if sel != nil && sel.WindowID != "" {
				_ = m.tmux.SelectWindow(sel.WindowID)
			}
		}

	case msg.String() == "ctrl+enter":
		if sel := m.list.Selected(); sel != nil {
			m.dialog.OpenContextMenu(sel, m.width/2-12, m.headerHeight()+2+m.list.Cursor())
		}

	case key.Matches(msg, m.keys.Filter):
		m.dialog.OpenFilter()

	case key.Matches(msg, m.keys.Profile):
		profiles, _ := config.ListProfiles(m.cfg.ProfileDir)
		m.dialog.OpenProfile(profiles)
	}

	return m, nil
}

func (m Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if msg.Action != tea.MouseActionPress {
		return m, nil
	}

	// Ignore clicks when a dialog is open (except context menu — close it)
	if m.dialog.Kind != DialogNone {
		if m.dialog.Kind == DialogContextMenu {
			m.dialog.Close()
		}
		return m, nil
	}

	// Header (variable lines) + resource bar (1) + table header (1) = offset.
	// Data rows start after that.
	contentRow := msg.Y - m.headerHeight() - 2 // -2: resource bar + table header
	if contentRow < 0 {
		return m, nil
	}

	idx := m.list.InstanceAtRow(contentRow)
	if idx < 0 {
		return m, nil
	}

	m.list.SetCursor(idx)

	switch msg.Button {
	case tea.MouseButtonLeft:
		// Attach — same logic as Enter key
		sel := m.list.Selected()
		if sel == nil {
			return m, nil
		}
		if sel.GroupName != "" {
			members := m.manager.GroupMembers(sel.GroupName)
			if len(members) > 1 {
				m.openTiledView(members)
				return m, nil
			}
		}
		if sel.Status == instance.StatusStopped || sel.Status == instance.StatusError {
			if err := m.launcher.Resume(sel.Name); err != nil {
				m.err = err
				m.errAt = time.Now()
				return m, nil
			}
			sel = m.manager.Get(sel.Name)
		}
		if sel != nil && sel.WindowID != "" {
			_ = m.tmux.SelectWindow(sel.WindowID)
		}

	case tea.MouseButtonRight:
		sel := m.list.Selected()
		if sel != nil {
			m.dialog.OpenContextMenu(sel, msg.X, msg.Y)
		}
	}

	return m, nil
}

func (m Model) handleDialogKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.dialog.Kind {
	case DialogNew:
		switch msg.String() {
		case "esc":
			m.dialog.Close()
			m.layout()
			return m, nil
		case "enter":
			// If dir completions are showing and one is highlighted, accept it
			if m.dialog.focus == 1 && m.dialog.dirCompIdx >= 0 {
				m.dialog.AcceptCompletion(m.dialog.dirCompIdx)
				return m, nil
			}
			name, dir, task, model, dangerous := m.dialog.NewInstanceValues()
			if name == "" {
				return m, nil
			}
			if dir == "" {
				dir = m.cfg.DefaultDir
			} else {
				dir = expandTilde(dir)
			}
			m.dialog.Close()
			m.layout()
			_, err := m.launcher.Launch(claude.LaunchOpts{
				Name:      name,
				Dir:       dir,
				Task:      task,
				Model:     model,
				Dangerous: dangerous,
			})
			if err != nil {
				m.err = err
				m.errAt = time.Now()
			}
			return m, nil
		case "down", "ctrl+n":
			// Navigate directory completions when in dir field
			if m.dialog.focus == 1 && len(m.dialog.dirCompletions) > 0 {
				if m.dialog.dirCompIdx < len(m.dialog.dirCompletions)-1 {
					m.dialog.dirCompIdx++
				}
				return m, nil
			}
			cmd := m.dialog.Update(msg)
			return m, cmd
		case "up", "ctrl+p":
			if m.dialog.focus == 1 && m.dialog.dirCompIdx >= 0 {
				m.dialog.dirCompIdx--
				return m, nil
			}
			cmd := m.dialog.Update(msg)
			return m, cmd
		default:
			cmd := m.dialog.Update(msg)
			return m, cmd
		}

	case DialogConfirmDelete:
		switch {
		case key.Matches(msg, key.NewBinding(key.WithKeys("y", "enter"))):
			target := m.dialog.Target()
			m.dialog.Close()
			if err := m.launcher.Delete(target); err != nil {
				m.err = err
				m.errAt = time.Now()
			}
		case key.Matches(msg, key.NewBinding(key.WithKeys("n", "esc"))):
			m.dialog.Close()
		}
		return m, nil

	case DialogConfirmBatchDelete:
		switch {
		case key.Matches(msg, key.NewBinding(key.WithKeys("y", "enter"))):
			targets := m.dialog.BatchTargets()
			m.dialog.Close()
			for _, name := range targets {
				if err := m.launcher.Delete(name); err != nil {
					m.err = err
					m.errAt = time.Now()
				}
			}
			m.list.ClearSelected()
		case key.Matches(msg, key.NewBinding(key.WithKeys("n", "esc"))):
			m.dialog.Close()
		}
		return m, nil

	case DialogProfile:
		switch msg.String() {
		case "esc":
			m.dialog.Close()
			return m, nil
		case "enter":
			profName := m.dialog.SelectedProfile()
			m.dialog.Close()
			if profName != "" {
				m.loadProfile(profName)
			}
			return m, nil
		default:
			cmd := m.dialog.Update(msg)
			return m, cmd
		}

	case DialogFilter:
		switch msg.String() {
		case "esc":
			m.list.SetFilter("")
			m.dialog.Close()
			return m, nil
		case "enter":
			m.list.SetFilter(m.dialog.FilterValue())
			m.dialog.Close()
			return m, nil
		default:
			cmd := m.dialog.Update(msg)
			m.list.SetFilter(m.dialog.FilterValue())
			return m, cmd
		}

	case DialogSessionPicker:
		switch msg.String() {
		case "esc":
			m.dialog.Close()
			return m, nil
		case "enter":
			sess := m.dialog.SelectedSession()
			target := m.dialog.Target()
			m.dialog.Close()
			if sess != nil {
				if err := m.launcher.ResumeSession(target, sess.SessionID); err != nil {
					m.err = err
					m.errAt = time.Now()
				}
			}
			return m, nil
		default:
			cmd := m.dialog.Update(msg)
			return m, cmd
		}

	case DialogContextMenu:
		switch msg.String() {
		case "esc":
			m.dialog.Close()
			return m, nil
		case "enter":
			action := m.dialog.SelectedMenuAction()
			target := m.dialog.Target()
			m.dialog.Close()
			return m.executeMenuAction(action, target)
		default:
			m.dialog.Update(msg)
			return m, nil
		}
	}

	return m, nil
}

// executeMenuAction dispatches a context menu action.
func (m Model) executeMenuAction(action, name string) (tea.Model, tea.Cmd) {
	switch action {
	case "attach":
		inst := m.manager.Get(name)
		if inst == nil {
			return m, nil
		}
		if inst.GroupName != "" {
			members := m.manager.GroupMembers(inst.GroupName)
			if len(members) > 1 {
				m.openTiledView(members)
				return m, nil
			}
		}
		if inst.WindowID != "" {
			_ = m.tmux.SelectWindow(inst.WindowID)
		}
	case "stop":
		if err := m.launcher.Kill(name); err != nil {
			m.err = err
			m.errAt = time.Now()
		}
	case "delete":
		m.dialog.OpenConfirmDelete(name)
	case "resume":
		if err := m.launcher.Resume(name); err != nil {
			m.err = err
			m.errAt = time.Now()
		}
	case "danger":
		if err := m.launcher.ToggleDangerous(name); err != nil {
			m.err = err
			m.errAt = time.Now()
		}
	case "ungroup":
		m.manager.ClearGroup([]string{name})
	}
	return m, nil
}

// openTiledView joins multiple instance windows into a single tiled pane layout.
func (m *Model) openTiledView(names []string) {
	// Collect instances that have live windows; resume stopped ones first
	var live []string
	for _, name := range names {
		inst := m.manager.Get(name)
		if inst == nil {
			continue
		}
		// Already running with a window — use it directly
		if inst.WindowID != "" && inst.Status != instance.StatusStopped && inst.Status != instance.StatusError {
			live = append(live, name)
			continue
		}
		// Stopped/error — try to resume
		if inst.Status == instance.StatusStopped || inst.Status == instance.StatusError {
			if err := m.launcher.Resume(name); err != nil {
				m.err = fmt.Errorf("resume %s: %w", name, err)
				m.errAt = time.Now()
				continue
			}
			inst = m.manager.Get(name) // re-fetch after resume
			if inst != nil && inst.WindowID != "" {
				live = append(live, name)
			}
		}
	}

	if len(live) == 0 {
		return
	}

	// Single instance: just switch to it
	if len(live) == 1 {
		inst := m.manager.Get(live[0])
		if inst != nil {
			_ = m.tmux.SelectWindow(inst.WindowID)
		}
		return
	}

	// Use the first instance's window as the base
	baseInst := m.manager.Get(live[0])
	if baseInst == nil {
		return
	}
	baseWinID := baseInst.WindowID
	origWindows := make(map[string]string, len(live))
	origWindows[live[0]] = baseWinID

	// Join remaining instances into the base window
	for _, name := range live[1:] {
		inst := m.manager.Get(name)
		if inst == nil || inst.WindowID == "" {
			continue
		}
		origWindows[name] = inst.WindowID
		if err := m.tmux.JoinPane(inst.WindowID, baseWinID); err != nil {
			m.err = err
			m.errAt = time.Now()
			continue
		}
		// After join-pane, the source window is destroyed;
		// the instance now lives as a pane in baseWinID
		inst.WindowID = baseWinID
	}

	// Apply tiled layout
	_ = m.tmux.SelectLayoutTiled(baseWinID)

	// Switch to the tiled view
	_ = m.tmux.SelectWindow(baseWinID)

	// Track tiled state for later break
	m.tiled = &tiledState{
		baseWindowID:    baseWinID,
		paneNames:       live,
		originalWindows: origWindows,
	}

	// Tell the launcher to skip status refresh for tiled instances
	m.launcher.TiledNames = make(map[string]bool, len(live))
	for _, name := range live {
		m.launcher.TiledNames[name] = true
	}
}

// breakTiledView splits a tiled view back into individual windows.
func (m *Model) breakTiledView() {
	if m.tiled == nil {
		return
	}

	panes, err := m.tmux.ListPanes(m.tiled.baseWindowID)
	if err != nil || len(panes) <= 1 {
		m.tiled = nil
		m.launcher.TiledNames = nil
		return
	}

	// Break all panes except the first one (which stays in the base window)
	// The first pane is the base instance — it keeps the original window
	baseInstance := m.tiled.paneNames[0]

	// Build a name queue for non-base panes
	nameIdx := 1 // start from second name
	for i := 1; i < len(panes) && nameIdx < len(m.tiled.paneNames); i++ {
		name := m.tiled.paneNames[nameIdx]
		nameIdx++

		newWinID, err := m.tmux.BreakPane(panes[i].PaneID, name)
		if err != nil {
			m.err = err
			m.errAt = time.Now()
			continue
		}

		// Update instance's window ID
		inst := m.manager.Get(name)
		if inst != nil {
			inst.WindowID = newWinID
			m.manager.SaveInstance(name)
		}
	}

	// The base instance keeps its original window ID
	baseInst := m.manager.Get(baseInstance)
	if baseInst != nil {
		baseInst.WindowID = m.tiled.baseWindowID
		m.manager.SaveInstance(baseInstance)
	}

	m.tiled = nil
	m.launcher.TiledNames = nil
}

func (m *Model) loadProfile(name string) {
	path := config.ProfilePath(m.cfg.ProfileDir, name)
	prof, err := config.LoadProfile(path)
	if err != nil {
		m.err = err
		m.errAt = time.Now()
		return
	}
	for _, pi := range prof.Instances {
		_, err := m.launcher.Launch(claude.LaunchOpts{
			Name:      pi.Name,
			Dir:       pi.Dir,
			Task:      pi.Task,
			Dangerous: pi.Dangerous,
		})
		if err != nil {
			m.err = err
			m.errAt = time.Now()
			return
		}
	}
}

func (m *Model) layout() {
	// Compute how many lines the shortcut bar needs
	m.shortcutRows = m.calcShortcutRows()

	// header + footer (1) = reserved rows
	contentH := m.height - m.headerHeight() - 1

	m.list.SetSize(m.width, contentH-1) // -1 for resource bar
	m.dialog.SetWidth(m.width)
	m.dialog.SetHeight(contentH)
	m.help.SetSize(m.width, m.height)

	// Side panel for new-instance form
	if m.dialog.IsSidePanel() {
		panelW := m.width * 45 / 100
		listW := m.width - panelW
		m.list.SetSize(listW, contentH-1)
		m.dialog.SetWidth(panelW)
		m.dialog.SetHeight(contentH)
	}
}

// expandTilde replaces a leading ~ with the user's home directory.
func expandTilde(path string) string {
	if strings.HasPrefix(path, "~/") || path == "~" {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, path[1:])
	}
	return path
}

// ASCII art logo for the top-right corner, styled like k9s.
var logoArt = []string{
	`╔═╗╦  ╔═╗╦ ╦╔╦╗╔═╗╔═╗`,
	`║  ║  ╠═╣║ ║ ║║║╣ ╚═╗`,
	`╚═╝╩═╝╩ ╩╚═╝═╩╝╚═╝╚═╝`,
}
var logoTagline = `» made by ARLINTDEV`

// renderHeader renders the compact header: logo + stats (3 lines) + shortcut bar (1 line).
func (m Model) renderHeader() string {
	total, running, idle, _, errored, _, _ := m.manager.Stats()

	cpuPct, memUsed, memTotal := getSysInfo()

	// --- Left side: info lines (3 to match logo height) ---
	info := []string{
		m.theme.HeaderLabel.Render("Instances: ") +
			m.theme.HeaderValue.Render(fmt.Sprintf("%d (%d running, %d idle, %d error)", total, running, idle, errored)),
		m.theme.HeaderLabel.Render("CPU:       ") +
			m.theme.HeaderValue.Render(fmt.Sprintf("%.0f%%", cpuPct)),
		m.theme.HeaderLabel.Render("MEM:       ") +
			m.theme.HeaderValue.Render(fmt.Sprintf("%s/%s", formatBytes(memUsed), formatBytes(memTotal))),
	}

	// --- Right side: logo + tagline ---
	var artLines []string
	for _, l := range logoArt {
		artLines = append(artLines, m.theme.Logo.Render(l))
	}
	// Right-align tagline under the logo
	tagline := m.theme.Muted.Render(logoTagline)
	artLines = append(artLines, tagline)

	// Pad info to same height
	for len(info) < len(artLines) {
		info = append(info, "")
	}

	artW := lipgloss.Width(artLines[0]) // logo width (widest line)
	var headerRows []string
	for i := 0; i < len(artLines); i++ {
		left := " " + info[i]
		leftW := lipgloss.Width(left)
		lineArtW := lipgloss.Width(artLines[i])
		// Right-align each art line to the logo width
		pad := artW - lineArtW
		artPadded := strings.Repeat(" ", pad) + artLines[i]
		gap := m.width - leftW - artW - 1
		if gap < 2 {
			gap = 2
		}
		headerRows = append(headerRows, left+strings.Repeat(" ", gap)+artPadded)
	}

	// --- Shortcut bar: wrap to terminal width ---
	var scParts []string
	for _, s := range shortcutLabels {
		scParts = append(scParts,
			m.theme.ShortcutKey.Render("<"+s.key+">")+
				m.theme.ShortcutDesc.Render(" "+s.desc))
	}

	// Wrap shortcuts into lines that fit the terminal width
	var scLines []string
	curLine := " "
	curW := 1
	sep := "  "
	sepW := 2
	for i, part := range scParts {
		partW := lipgloss.Width(part)
		addW := partW
		if i > 0 && curW > 1 {
			addW += sepW
		}
		if curW+addW > m.width && curW > 1 {
			scLines = append(scLines, curLine)
			curLine = " " + part
			curW = 1 + partW
		} else {
			if i > 0 && curW > 1 {
				curLine += sep
				curW += sepW
			}
			curLine += part
			curW += partW
		}
	}
	if curW > 1 {
		scLines = append(scLines, curLine)
	}
	headerRows = append(headerRows, scLines...)

	return strings.Join(headerRows, "\n")
}

// renderResourceBar renders the centered resource type bar (like k9s's "Pods(all)[137]").
func (m Model) renderResourceBar(width int) string {
	total, _, _, _, _, _, _ := m.manager.Stats()

	filterInfo := "all"
	if m.list.filter != "" {
		filterInfo = m.list.filter
	}

	label := fmt.Sprintf(" Instances(%s)[%d] ", filterInfo, total)
	if sel := m.list.SelectedCount(); sel > 0 {
		label = fmt.Sprintf(" Instances(%s)[%d] %d selected ", filterInfo, total, sel)
	}
	styledLabel := m.theme.Logo.Render(label)
	labelW := lipgloss.Width(styledLabel)

	// Center the label with ─── on each side
	sideW := (width - labelW) / 2
	if sideW < 0 {
		sideW = 0
	}
	rightW := width - sideW - labelW
	if rightW < 0 {
		rightW = 0
	}
	leftBar := m.theme.Muted.Render(strings.Repeat("─", sideW))
	rightBar := m.theme.Muted.Render(strings.Repeat("─", rightW))

	return leftBar + styledLabel + rightBar
}

// renderFooter renders the bottom status bar.
func (m Model) renderFooter() string {
	if m.dialog.IsInlineDialog() {
		return m.dialog.View()
	}
	if m.err != nil {
		return m.theme.ErrorFlash.Width(m.width).Render("Error: " + m.err.Error())
	}
	return ""
}

// View renders the entire UI.
func (m Model) View() string {
	// Multi-line header: context info left, shortcuts grid right
	header := m.renderHeader()

	// Content area
	contentH := m.height - m.headerHeight() - 1
	listView := m.list.View()

	var content string
	if m.dialog.IsSidePanel() {
		panelW := m.width * 45 / 100
		listW := m.width - panelW
		resourceBar := m.renderResourceBar(listW)
		leftCol := lipgloss.JoinVertical(lipgloss.Left, resourceBar, listView)
		leftCol = lipgloss.NewStyle().Width(listW).Height(contentH).Render(leftCol)
		rightCol := m.dialog.View()
		content = lipgloss.JoinHorizontal(lipgloss.Top, leftCol, rightCol)
	} else {
		resourceBar := m.renderResourceBar(m.width)
		content = lipgloss.JoinVertical(lipgloss.Left, resourceBar, listView)
		content = lipgloss.NewStyle().Width(m.width).Height(contentH).Render(content)
	}

	// Footer
	footer := m.renderFooter()

	// Build base view
	base := lipgloss.JoinVertical(lipgloss.Left, header, content, footer)

	// Context menu: position at click location
	if m.dialog.Kind == DialogContextMenu {
		dialog := m.dialog.View()
		return m.overlayAt(base, dialog, m.dialog.menuX, m.dialog.menuY)
	}

	// Modal overlays centered on screen (non-inline, non-side-panel dialogs)
	if m.dialog.Kind != DialogNone && !m.dialog.IsInlineDialog() && !m.dialog.IsSidePanel() {
		dialog := m.dialog.View()
		return m.overlay(base, dialog)
	}

	if m.help.Visible() {
		helpView := m.help.View()
		return m.overlay(base, helpView)
	}

	return base
}

// overlayAt places a dialog at specific coordinates, clamped to screen bounds.
// Uses ANSI-aware truncation so styled base lines aren't corrupted.
func (m Model) overlayAt(base, dialog string, x, y int) string {
	dialogW := lipgloss.Width(dialog)
	dialogH := lipgloss.Height(dialog)

	// Clamp to screen
	if x+dialogW > m.width {
		x = m.width - dialogW
	}
	if y+dialogH > m.height {
		y = m.height - dialogH
	}
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}

	// Pad base to fill the full terminal height
	baseLines := strings.Split(base, "\n")
	for len(baseLines) < m.height {
		baseLines = append(baseLines, strings.Repeat(" ", m.width))
	}

	dialogLines := strings.Split(dialog, "\n")

	for i, dl := range dialogLines {
		row := y + i
		if row >= len(baseLines) {
			break
		}
		line := baseLines[row]
		// ANSI-aware: truncate to x visible chars for prefix
		prefix := ansi.Truncate(line, x, "")
		// ANSI-aware: drop the first (x + dialogW) visible chars for suffix
		suffix := ansi.TruncateLeft(line, x+dialogW, "")
		baseLines[row] = prefix + dl + suffix
	}
	return strings.Join(baseLines, "\n")
}

// overlay centers a dialog on top of the base view.
func (m Model) overlay(base, dialog string) string {
	dialogW := lipgloss.Width(dialog)
	dialogH := lipgloss.Height(dialog)

	x := (m.width - dialogW) / 2
	y := (m.height - dialogH) / 3

	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}

	// Build the overlay by splitting base into lines and replacing
	baseLines := strings.Split(base, "\n")
	dialogLines := strings.Split(dialog, "\n")

	for i, dl := range dialogLines {
		row := y + i
		if row >= len(baseLines) {
			break
		}
		line := baseLines[row]
		// Pad line if needed
		for len(line) < x+len(dl) {
			line += " "
		}

		prefix := ""
		if x > 0 && x < len(line) {
			prefix = line[:x]
		}
		suffix := ""
		end := x + dialogW
		if end < len(line) {
			suffix = line[end:]
		}
		baseLines[row] = prefix + dl + suffix
	}

	return strings.Join(baseLines, "\n")
}
