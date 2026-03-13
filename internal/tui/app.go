package tui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/arlintdev/claudes/internal/claude"
	"github.com/arlintdev/claudes/internal/config"
	"github.com/arlintdev/claudes/internal/instance"
	"github.com/arlintdev/claudes/internal/tmux"
	"github.com/arlintdev/claudes/internal/update"
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// baseHeaderLines is the fixed portion: logo/info (3 lines) + tagline (1 line) + resource bar (1 line).
const baseHeaderLines = 5

// Messages
type tickMsg time.Time
type statusMsg struct{}
type errMsg struct{ err error }
type updateAvailableMsg struct{ release *update.Release }
type updateAppliedMsg struct{ err error }

// previewPaneState tracks an active tmux split preview.
type previewPaneState struct {
	paneID           string // tmux pane ID (e.g. "%5")
	instanceID       string // instance being previewed
	originalWindowID string // instance's window ID before join (now destroyed)
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
	version      string     // current build version (e.g. "v0.5.0" or "dev")

	previewPane *previewPaneState // active tmux split, nil when no instance attached
}

// New creates the root model.
func New(cfg config.Config, mgr *instance.Manager, tc *tmux.Client, launcher *claude.Launcher, sessions *claude.SessionStore, version string) Model {
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
		version:  version,
	}
}

// shortcutLabels are the key/desc pairs for the shortcut bar.
var shortcutLabels = []struct{ key, desc string }{
	{"n", "New"}, {"m", "Mode"}, {"s", "Stop"}, {"x", "Stop Idle"}, {"d", "Delete"},
	{"Enter", "Focus"}, {"^r", "Resume"}, {"Space", "Select"}, {"^a", "All"},
	{"g", "Group"}, {"/", "Filter"}, {"^Space", "Back"}, {"?", "Help"}, {"q", "Quit"},
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

// headerHeight returns the total header lines (info + resource bar, shortcuts are at the bottom).
func (m Model) headerHeight() int {
	return baseHeaderLines
}

// Init starts the tick timer and checks for updates.
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.tick(), m.checkForUpdate())
}

func (m Model) checkForUpdate() tea.Cmd {
	version := m.version
	return func() tea.Msg {
		rel := update.CheckLatest(version)
		return updateAvailableMsg{release: rel}
	}
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
			for id, line := range m.launcher.Activity.All() {
				m.list.SetActivity(id, line)
			}
			for id, state := range m.launcher.Activity.AllStates() {
				m.list.SetActivityState(id, state)
			}
		}

		return m, m.tick()

	case updateAvailableMsg:
		if msg.release != nil {
			m.dialog.OpenUpdate(m.version, msg.release.Version, msg.release.DownloadURL)
		}
		return m, nil

	case updateAppliedMsg:
		if msg.err != nil {
			m.dialog.updateDownloading = false
			m.dialog.updateErr = msg.err
			return m, nil
		}
		// Update succeeded — re-exec the binary
		exe, err := os.Executable()
		if err != nil {
			m.dialog.updateDownloading = false
			m.dialog.updateErr = err
			return m, nil
		}
		return m, tea.ExecProcess(exec.Command(exe), func(err error) tea.Msg {
			return errMsg{err: err}
		})

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
		m.closePreview() // clean up tmux split before exit
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
		if m.previewPane != nil {
			m.syncPreview()
		}

	case key.Matches(msg, m.keys.Down):
		m.list.MoveDown()
		if m.previewPane != nil {
			m.syncPreview()
		}

	case key.Matches(msg, m.keys.New):
		m.dialog.OpenNew()
		m.layout()

	case msg.String() == " ":
		m.list.ToggleSelected()

	case key.Matches(msg, m.keys.SelectAll):
		m.list.SelectAll()

	case key.Matches(msg, m.keys.Group):
		if ids := m.list.SelectedIDs(); len(ids) >= 2 {
			m.dialog.OpenGroupName(ids)
		} else if ids := m.list.SelectedIDs(); ids != nil {
			m.manager.ClearGroup(ids)
			m.list.ClearSelected()
		} else if sel := m.list.Selected(); sel != nil && sel.GroupName != "" {
			m.manager.ClearGroup([]string{sel.ID})
		}

	case key.Matches(msg, m.keys.Stop):
		if ids := m.list.SelectedIDs(); ids != nil {
			// Batch stop
			for _, id := range ids {
				inst := m.manager.Get(id)
				if inst != nil && inst.Status != instance.StatusStopped {
					// Close preview if we're stopping the previewed instance
					if m.previewPane != nil && m.previewPane.instanceID == id {
						m.closePreview()
					}
					if err := m.launcher.Kill(id); err != nil {
						m.err = err
						m.errAt = time.Now()
					}
				}
			}
			m.list.ClearSelected()
		} else if sel := m.list.Selected(); sel != nil && sel.Status != instance.StatusStopped {
			// Close preview if we're stopping the previewed instance
			if m.previewPane != nil && m.previewPane.instanceID == sel.ID {
				m.closePreview()
			}
			if err := m.launcher.Kill(sel.ID); err != nil {
				m.err = err
				m.errAt = time.Now()
			}
		}
		m.list.Sync(m.manager.All())

	case key.Matches(msg, m.keys.StopIdle):
		for _, inst := range m.manager.All() {
			if inst.Status == instance.StatusIdle {
				if err := m.launcher.Kill(inst.ID); err != nil {
					m.err = err
					m.errAt = time.Now()
				}
			}
		}

	case key.Matches(msg, m.keys.Delete):
		if ids := m.list.SelectedIDs(); ids != nil {
			m.dialog.OpenConfirmBatchDelete(ids)
		} else if sel := m.list.Selected(); sel != nil {
			m.dialog.OpenConfirmDelete(sel.ID, sel.Name)
		}

	case key.Matches(msg, m.keys.Mode):
		if sel := m.list.Selected(); sel != nil {
			if err := m.launcher.ToggleDangerous(sel.ID); err != nil {
				m.err = err
				m.errAt = time.Now()
			}
		}

	case key.Matches(msg, m.keys.Resume):
		if sel := m.list.Selected(); sel != nil {
			if sel.Status == instance.StatusStopped || sel.Status == instance.StatusError {
				if err := m.launcher.Resume(sel.ID); err != nil {
					m.err = err
					m.errAt = time.Now()
				}
				m.list.Sync(m.manager.All())
			}
		}

	case key.Matches(msg, m.keys.Attach):
		if sel := m.list.Selected(); sel != nil {
			if sel.Status == instance.StatusStopped || sel.Status == instance.StatusError {
				if err := m.launcher.Resume(sel.ID); err != nil {
					m.err = err
					m.errAt = time.Now()
					return m, nil
				}
				// Re-sync so the resumed instance moves to the active section
				m.list.Sync(m.manager.All())
			}
		}
		m.syncPreview()
		if m.previewPane != nil {
			_ = m.tmux.FocusPane(m.previewPane.paneID)
		}

	case key.Matches(msg, m.keys.Filter):
		m.dialog.OpenFilter()
	}

	return m, nil
}

func (m Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if msg.Action != tea.MouseActionPress {
		return m, nil
	}

	// Ignore clicks when a dialog is open
	if m.dialog.Kind != DialogNone {
		return m, nil
	}

	// Header (variable lines) + resource bar (1) = offset.
	// Card rows start after that.
	contentRow := msg.Y - m.headerHeight() - 1 // -1: resource bar
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
		// Attach — same logic as Enter key (single instance)
		sel := m.list.Selected()
		if sel == nil {
			return m, nil
		}
		if sel.Status == instance.StatusStopped || sel.Status == instance.StatusError {
			if err := m.launcher.Resume(sel.ID); err != nil {
				m.err = err
				m.errAt = time.Now()
				return m, nil
			}
			sel = m.manager.Get(sel.ID)
		}
		if sel != nil && sel.WindowID != "" {
			_ = m.tmux.SelectWindow(sel.WindowID)
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
			name, dir, task, model, host, dangerous, resume := m.dialog.NewInstanceValues()
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
				Host:      host,
				Dangerous: dangerous,
				Resume:    resume,
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

	case DialogGroupName:
		switch msg.String() {
		case "esc":
			m.dialog.Close()
			return m, nil
		case "enter":
			groupName := m.dialog.GroupNameValue()
			ids := m.dialog.GroupTargetIDs()
			m.dialog.Close()
			if groupName != "" && len(ids) > 0 {
				m.manager.SetGroup(ids, groupName)
				m.list.ClearSelected()
			}
			return m, nil
		default:
			cmd := m.dialog.Update(msg)
			return m, cmd
		}

	case DialogUpdate:
		if m.dialog.updateDownloading {
			return m, nil // ignore input while downloading
		}
		switch msg.String() {
		case "esc", "n":
			m.dialog.Close()
			return m, nil
		case "enter":
			if m.dialog.updateCur == 1 || m.dialog.updateErr != nil {
				// Skip or dismiss error
				m.dialog.Close()
				return m, nil
			}
			// Start download
			m.dialog.updateDownloading = true
			url := m.dialog.updateURL
			return m, func() tea.Msg {
				err := update.Apply(url)
				return updateAppliedMsg{err: err}
			}
		default:
			m.dialog.Update(msg)
			return m, nil
		}

	}

	return m, nil
}

// syncPreview updates the preview split to show the currently selected instance.
// Skips stopped instances (keeps showing the last running one).
// Uses swap-pane when a split already exists (instant), join-pane to create the first split.
func (m *Model) syncPreview() {
	sel := m.list.Selected()
	if sel == nil || sel.WindowID == "" {
		// Stopped or no selection — close preview to restore full dashboard
		m.closePreview()
		return
	}
	if m.previewPane != nil && sel.ID == m.previewPane.instanceID {
		return // already showing
	}

	if m.previewPane != nil {
		// Split exists — swap (fast path)
		err := m.tmux.SwapPane(m.previewPane.paneID, sel.WindowID)
		if err != nil {
			return // silently skip on error — don't flash during navigation
		}
		oldInst := m.manager.Get(m.previewPane.instanceID)
		if oldInst != nil {
			oldInst.WindowID = sel.WindowID
			m.manager.SaveInstance(m.previewPane.instanceID)
		}
		// Old instance gets its window back — clear previewing for it
		m.launcher.PreviewingID = sel.ID
		panes, _ := m.tmux.ListPanes(tmux.DashboardWindowName)
		var newPaneID string
		if len(panes) >= 2 {
			newPaneID = panes[len(panes)-1].PaneID
		}
		m.previewPane = &previewPaneState{
			paneID:           newPaneID,
			instanceID:       sel.ID,
			originalWindowID: sel.WindowID,
		}
	} else {
		// No split yet — create one
		paneID, err := m.tmux.JoinPaneRight(sel.WindowID, tmux.DashboardWindowName, 75)
		if err != nil {
			return
		}
		m.previewPane = &previewPaneState{
			paneID:           paneID,
			instanceID:       sel.ID,
			originalWindowID: sel.WindowID,
		}
		m.launcher.PreviewingID = sel.ID
		_ = m.tmux.FocusDashboardPane()
	}
}

// closePreview breaks the previewed pane back into its own window.
func (m *Model) closePreview() {
	if m.previewPane == nil {
		return
	}
	newWinID, err := m.tmux.BreakPane(m.previewPane.paneID, m.previewPane.instanceID)
	if err != nil {
		m.err = err
		m.errAt = time.Now()
		m.previewPane = nil
		return
	}
	inst := m.manager.Get(m.previewPane.instanceID)
	if inst != nil {
		inst.WindowID = newWinID
		m.manager.SaveInstance(m.previewPane.instanceID)
	}
	m.launcher.PreviewingID = ""
	m.previewPane = nil
}

func (m *Model) layout() {
	// Compute how many lines the shortcut bar needs
	m.shortcutRows = m.calcShortcutRows()

	// header + shortcuts (bottom) + footer (1) = reserved rows
	contentH := m.height - m.headerHeight() - m.shortcutRows - 1

	m.list.SetSize(m.width, contentH-1) // -1 for resource bar
	m.dialog.SetWidth(m.width)
	m.dialog.SetHeight(contentH)
	m.help.SetSize(m.width, m.height)
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
var logoTagline = `~/made/by/arlint.dev`

// renderHeader renders the header: logo + tagline (3 lines), then a stats line below.
func (m Model) renderHeader() string {
	_, running, idle, _, errored, _, _ := m.manager.Stats()
	cpuPct, memUsed, memTotal := getSysInfo()

	// Logo lines
	var rows []string
	for _, l := range logoArt {
		rows = append(rows, " "+m.theme.Logo.Render(l))
	}

	// Tagline
	tag := logoTagline
	if m.version != "" {
		tag += " " + m.version
	}
	rows = append(rows, " "+m.theme.Muted.Render(tag))

	// Stats on a single line
	stats := " " + m.theme.HeaderValue.Render(
		fmt.Sprintf("%dR %dI %dE  cpu %.0f%%  mem %s/%s",
			running, idle, errored, cpuPct, formatBytes(memUsed), formatBytes(memTotal)))
	rows = append(rows, stats)

	return strings.Join(rows, "\n")
}

// renderShortcuts renders the shortcut bar, wrapped to terminal width.
func (m Model) renderShortcuts() string {
	var scParts []string
	for _, s := range shortcutLabels {
		scParts = append(scParts,
			m.theme.ShortcutKey.Render("<"+s.key+">")+
				m.theme.ShortcutDesc.Render(" "+s.desc))
	}

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

	return strings.Join(scLines, "\n")
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
	contentH := m.height - m.headerHeight() - m.shortcutRows - 1
	listView := m.list.View()

	resourceBar := m.renderResourceBar(m.width)
	contentParts := []string{resourceBar, listView}
	if m.dialog.IsContentDialog() {
		contentParts = append(contentParts, m.dialog.View())
	}
	content := lipgloss.JoinVertical(lipgloss.Left, contentParts...)
	content = lipgloss.NewStyle().Width(m.width).Height(contentH).Render(content)

	// Shortcuts pinned to bottom
	shortcuts := m.renderShortcuts()

	// Footer
	footer := m.renderFooter()

	// Build base view
	base := lipgloss.JoinVertical(lipgloss.Left, header, content, shortcuts, footer)

	// Modal overlays centered on screen (non-inline, non-content, non-side-panel dialogs)
	if m.dialog.Kind != DialogNone && !m.dialog.IsInlineDialog() && !m.dialog.IsContentDialog() && !m.dialog.IsSidePanel() {
		dialog := m.dialog.View()
		return m.overlay(base, dialog)
	}

	if m.help.Visible() {
		helpView := m.help.View()
		return m.overlay(base, helpView)
	}

	return base
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
