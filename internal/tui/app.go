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
	"github.com/arlintdev/claudes/internal/daemon"
	"github.com/arlintdev/claudes/internal/instance"
	"github.com/arlintdev/claudes/internal/update"
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// baseHeaderLines is the fixed portion: logo/info (3 lines) + tagline (1 line) + resource bar (1 line).
const baseHeaderLines = 5

// Messages
type tickMsg time.Time
type statusMsg struct{}
type errMsg struct{ err error }
type updateAvailableMsg struct{ release *update.Release }
type updateAppliedMsg struct{ err error }

// paneMode tracks what the right pane shows.
type paneMode int

const (
	paneModeInfo     paneMode = iota // instance details
	paneModeTerminal                 // live Claude terminal
)

// Model is the root Bubble Tea model.
type Model struct {
	keys     KeyMap
	theme    Theme
	cfg      config.Config
	list     ListView
	dialog   Dialog
	help     HelpOverlay
	manager  *instance.Manager
	launcher *claude.Launcher
	daemon   *daemon.Client
	preview  *PreviewPane
	sessions *claude.SessionStore
	pane     paneMode

	width        int
	height       int
	shortcutRows int // number of lines the shortcut bar occupies
	err          error
	errAt        time.Time // when error was set, for flash bar timeout
	version      string    // current build version (e.g. "v0.5.0" or "dev")
}

// New creates the root model.
func New(cfg config.Config, mgr *instance.Manager, dc *daemon.Client, launcher *claude.Launcher, sessions *claude.SessionStore, version string) Model {
	theme := DefaultTheme()
	return Model{
		keys:     DefaultKeyMap(),
		theme:    theme,
		cfg:      cfg,
		list:     NewListView(theme),
		dialog:   NewDialog(theme),
		help:     NewHelpOverlay(theme),
		manager:  mgr,
		launcher: launcher,
		daemon:   dc,
		preview:  NewPreviewPane(dc),
		sessions: sessions,
		version:  version,
	}
}

// shortcutLabels are the key/desc pairs for the shortcut bar.
var shortcutLabels = []struct{ key, desc string }{
	{"n", "New"}, {"m", "Mode"}, {"s", "Stop"}, {"x", "Stop Idle"}, {"d", "Delete"},
	{"Enter", "Focus"}, {"^Space", "Back"}, {"^r", "Resume"}, {"Space", "Select"}, {"^a", "All"},
	{"g", "Group"}, {"/", "Filter"}, {"?", "Help"}, {"q", "Quit"},
}

// calcShortcutRows computes how many lines the shortcut bar needs at current width.
func (m Model) calcShortcutRows() int {
	if m.width <= 0 {
		return 1
	}
	rows := 1
	curW := 1 // leading space
	for i, s := range shortcutLabels {
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

// headerHeight returns the total header lines.
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

// paneWidth returns the width allocated to the right pane (always 70%).
func (m Model) paneWidth() int {
	return m.width * 70 / 100
}

// menuWidth returns the width allocated to the menu (left side).
func (m Model) menuWidth() int {
	return m.width - m.paneWidth() - 1 // 1 for separator
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
		var cmds []tea.Cmd
		if m.preview.IsAttached() {
			cmds = append(cmds, m.preview.Resize(m.paneWidth(), m.contentHeight()))
		}
		return m, tea.Batch(cmds...)

	case tickMsg:
		m.launcher.RefreshStatuses()
		m.list.Sync(m.manager.All())
		m.list.Tick()

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

	case previewAttachedMsg:
		// Preview connected — initialize bubbleterm polling
		return m, m.preview.Init()

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

	case tea.MouseClickMsg:
		return m.handleMouseClick(msg)

	case tea.KeyPressMsg:
		// If pane is in terminal mode, forward keys except ctrl+space and ctrl+n/ctrl+p
		if m.pane == paneModeTerminal {
			switch msg.String() {
			case "ctrl+space", "ctrl+@":
				m.detachTerminal()
				return m, nil
			case "ctrl+p":
				m.list.MoveUp()
				return m, m.focusInstance()
			case "ctrl+n":
				m.list.MoveDown()
				return m, m.focusInstance()
			default:
				// Send raw input to daemon PTY only — output echoes back
				// via subscription to BubbleTerm. Don't also pass to
				// BubbleTerm's Update or input gets doubled.
				if data := keyToBytes(msg); data != nil {
					m.preview.SendInput(data)
				}
				return m, nil
			}
		}
		return m.handleKey(msg)
	}

	// Forward terminal-related messages to preview
	if m.preview.IsAttached() {
		cmd := m.preview.Update(msg)
		if cmd != nil {
			return m, cmd
		}
	}

	return m, nil
}

// keyToBytes converts a key press to raw terminal bytes for the daemon PTY.
func keyToBytes(msg tea.KeyPressMsg) []byte {
	s := msg.String()
	switch s {
	case "enter":
		return []byte("\r")
	case "tab":
		return []byte("\t")
	case "backspace":
		return []byte{0x7f}
	case "esc":
		return []byte{0x1b}
	case "up":
		return []byte("\x1b[A")
	case "down":
		return []byte("\x1b[B")
	case "right":
		return []byte("\x1b[C")
	case "left":
		return []byte("\x1b[D")
	case "ctrl+c":
		return []byte{0x03}
	case "ctrl+d":
		return []byte{0x04}
	case "ctrl+z":
		return []byte{0x1a}
	case "ctrl+l":
		return []byte{0x0c}
	case "space":
		return []byte(" ")
	default:
		if len(s) == 1 {
			return []byte(s)
		}
		// For multi-byte characters
		if msg.Text != "" {
			return []byte(msg.Text)
		}
		return nil
	}
}

func (m Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
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
		m.detachTerminal()
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

	case msg.String() == "space":
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
			for _, id := range ids {
				inst := m.manager.Get(id)
				if inst != nil && inst.Status != instance.StatusStopped {
					if m.preview.InstanceID() == id {
						m.detachTerminal()
					}
					if err := m.launcher.Kill(id); err != nil {
						m.err = err
						m.errAt = time.Now()
					}
				}
			}
			m.list.ClearSelected()
		} else if sel := m.list.Selected(); sel != nil && sel.Status != instance.StatusStopped {
			if m.preview.InstanceID() == sel.ID {
				m.detachTerminal()
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
			if err := m.launcher.ToggleDangerous(sel.ID, m.paneWidth(), m.height); err != nil {
				m.err = err
				m.errAt = time.Now()
			}
		}

	case key.Matches(msg, m.keys.Resume):
		if sel := m.list.Selected(); sel != nil {
			if sel.Status == instance.StatusStopped || sel.Status == instance.StatusError {
				if err := m.launcher.Resume(sel.ID, m.paneWidth(), m.contentHeight()); err != nil {
					m.err = fmt.Errorf("resume %s: %w", sel.Name, err)
					m.errAt = time.Now()
				}
				m.launcher.RefreshStatuses()
				m.list.Sync(m.manager.All())
			}
		}

	case key.Matches(msg, m.keys.Attach):
		return m, m.focusInstance()

	case key.Matches(msg, m.keys.Filter):
		m.dialog.OpenFilter()
	}

	return m, nil
}

func (m Model) handleMouseClick(msg tea.MouseClickMsg) (tea.Model, tea.Cmd) {
	// Ignore clicks when a dialog is open
	if m.dialog.Kind != DialogNone {
		return m, nil
	}

	// Header offset (header + resource bar)
	contentRow := msg.Y - m.headerHeight() - 2
	if contentRow < 0 {
		return m, nil
	}

	idx := m.list.InstanceAtRow(contentRow)
	if idx < 0 {
		return m, nil
	}

	m.list.SetCursor(idx)

	if msg.Button == tea.MouseLeft {
		return m, m.focusInstance()
	}

	return m, nil
}

func (m Model) handleDialogKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch m.dialog.Kind {
	case DialogNew:
		switch msg.String() {
		case "esc":
			m.dialog.Close()
			m.layout()
			return m, nil
		case "enter":
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
				Cols:      m.paneWidth(),
				Rows:      m.contentHeight(),
			})
			if err != nil {
				m.err = err
				m.errAt = time.Now()
			}
			return m, nil
		case "down", "ctrl+n":
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
			if m.preview.InstanceID() == target {
				m.detachTerminal()
			}
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
			for _, id := range targets {
				if m.preview.InstanceID() == id {
					m.detachTerminal()
				}
				if err := m.launcher.Delete(id); err != nil {
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
				if err := m.launcher.ResumeSession(target, sess.SessionID, m.paneWidth(), m.height); err != nil {
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
			return m, nil
		}
		switch msg.String() {
		case "esc", "n":
			m.dialog.Close()
			return m, nil
		case "enter":
			if m.dialog.updateCur == 1 || m.dialog.updateErr != nil {
				m.dialog.Close()
				return m, nil
			}
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

// focusInstance resumes the selected instance if stopped, then attaches the terminal pane.
// This is the single entry point for Enter, mouse click, ctrl+n/p, and ctrl+1-9.
func (m *Model) focusInstance() tea.Cmd {
	sel := m.list.Selected()
	if sel == nil {
		return nil
	}
	id := sel.ID

	// Resume stopped/errored instances
	if sel.Status == instance.StatusStopped || sel.Status == instance.StatusError {
		if err := m.launcher.Resume(id, m.paneWidth(), m.contentHeight()); err != nil {
			m.err = fmt.Errorf("resume %s: %w", sel.Name, err)
			m.errAt = time.Now()
			return nil
		}
		m.launcher.RefreshStatuses()
		m.list.Sync(m.manager.All())
	}

	// Detach previous if different
	if m.preview.InstanceID() != id {
		m.preview.Detach()
	}

	// If already attached to same instance, just focus
	if m.preview.InstanceID() == id {
		m.pane = paneModeTerminal
		m.preview.Focus()
		m.list.previewFocused = true
		return nil
	}

	pw := m.paneWidth()
	if pw < 40 {
		pw = 40
	}
	ph := m.contentHeight()
	cmd := m.preview.Attach(id, pw, ph)
	m.pane = paneModeTerminal
	m.preview.Focus()
	m.list.previewFocused = true
	return cmd
}

// detachTerminal switches the pane back to info mode.
func (m *Model) detachTerminal() {
	m.preview.Blur()
	m.preview.Detach()
	m.pane = paneModeInfo
	m.list.previewFocused = false
}

// contentHeight returns the height available for the list+preview area.
func (m Model) contentHeight() int {
	// header (5) + resource bar (1) + middle (contentH) + shortcuts (shortcutRows) + footer (1)
	h := m.height - m.headerHeight() - 1 - m.shortcutRows - 1
	if h < 1 {
		h = 1
	}
	return h
}

func (m *Model) layout() {
	m.shortcutRows = m.calcShortcutRows()
	dw := m.menuWidth()
	contentH := m.contentHeight()
	m.list.SetSize(dw, contentH-1)
	m.dialog.SetWidth(dw)
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

// ASCII art logo
var logoArt = []string{
	`╔═╗╦  ╔═╗╦ ╦╔╦╗╔═╗╔═╗`,
	`║  ║  ╠═╣║ ║ ║║║╣ ╚═╗`,
	`╚═╝╩═╝╩ ╩╚═╝═╩╝╚═╝╚═╝`,
}
var logoTagline = `~/made/by/arlint.dev`

func (m Model) renderHeader() string {
	_, running, idle, _, errored, _, _ := m.manager.Stats()
	cpuPct, memUsed, memTotal := getSysInfo()

	var rows []string
	for _, l := range logoArt {
		rows = append(rows, " "+m.theme.Logo.Render(l))
	}

	tag := logoTagline
	if m.version != "" {
		tag += " " + m.version
	}
	rows = append(rows, " "+m.theme.Muted.Render(tag))

	stats := " " + m.theme.HeaderValue.Render(
		fmt.Sprintf("%dR %dI %dE  cpu %.0f%%  mem %s/%s",
			running, idle, errored, cpuPct, formatBytes(memUsed), formatBytes(memTotal)))
	rows = append(rows, stats)

	return strings.Join(rows, "\n")
}

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

func (m Model) renderFooter() string {
	if m.dialog.IsInlineDialog() {
		return m.dialog.View()
	}
	if m.err != nil {
		return m.theme.ErrorFlash.Width(m.width).Render("Error: " + m.err.Error())
	}
	return ""
}

// renderInfoPane renders instance details for the right pane.
func (m Model) renderInfoPane(width, height int) string {
	sel := m.list.Selected()
	if sel == nil {
		return lipgloss.NewStyle().
			Width(width).Height(height).
			Foreground(m.theme.Muted.GetForeground()).
			Padding(1, 2).
			Render("No instance selected")
	}

	var lines []string

	// Name
	lines = append(lines, m.theme.Bold.Render(sel.Name))
	lines = append(lines, "")

	// Status
	dot := m.list.StatusDot(sel.Status)
	lines = append(lines, dot+" "+m.list.StyleStatus(sel.Status))

	// Mode
	lines = append(lines, m.theme.Label.Render("Mode: ")+m.list.StyleMode(sel.Mode))

	// Directory
	lines = append(lines, m.theme.Label.Render("Dir:  ")+m.theme.Muted.Render(sel.Dir))

	// Model
	if sel.Model != "" {
		lines = append(lines, m.theme.Label.Render("Model: ")+sel.Model)
	}

	// Host
	if sel.Host != "" && sel.Host != "local" {
		lines = append(lines, m.theme.Label.Render("Host: ")+sel.Host)
	}

	// Task
	if sel.Task != "" {
		task := sel.Task
		if len(task) > width-8 {
			task = task[:width-11] + "..."
		}
		lines = append(lines, m.theme.Label.Render("Task: ")+m.theme.Muted.Render(task))
	}

	// Session ID
	if sel.SessionID != "" {
		lines = append(lines, m.theme.Label.Render("Session: ")+m.theme.Muted.Render(sel.SessionID))
	}

	// Uptime
	if sel.Status != instance.StatusStopped {
		lines = append(lines, m.theme.Label.Render("Uptime: ")+sel.Uptime())
	}

	// Activity
	if state := m.list.ActivityState(sel.ID); state != nil {
		lines = append(lines, "")
		if state.CostUSD > 0 {
			lines = append(lines, m.theme.Label.Render("Cost: ")+fmt.Sprintf("$%.2f", state.CostUSD))
		}
		if state.TokensIn > 0 || state.TokensOut > 0 {
			lines = append(lines, m.theme.Label.Render("Tokens: ")+
				fmt.Sprintf("%s in / %s out", formatTokens(state.TokensIn), formatTokens(state.TokensOut)))
		}
	}

	// Hint
	lines = append(lines, "")
	if sel.DaemonPID != 0 {
		lines = append(lines, m.theme.Muted.Render("Press Enter to open terminal"))
	} else {
		lines = append(lines, m.theme.Muted.Render("Press Enter to resume"))
	}

	content := strings.Join(lines, "\n")
	return lipgloss.NewStyle().Width(width).Height(height).Padding(1, 2).Render(content)
}

// View renders the entire UI.
func (m Model) View() tea.View {
	dw := m.menuWidth()
	pw := m.paneWidth()
	contentH := m.contentHeight()

	// Full-width header
	header := m.renderHeader()

	// Full-width resource bar
	resourceBar := m.renderResourceBar(m.width)

	// Build instance list column (menu)
	listView := m.list.View()
	listParts := []string{listView}
	if m.dialog.IsContentDialog() {
		listParts = append(listParts, m.dialog.View())
	}
	listContent := lipgloss.JoinVertical(lipgloss.Left, listParts...)
	listContent = lipgloss.NewStyle().Width(dw).Height(contentH).Render(listContent)

	// Build right pane: info or terminal
	var paneContent string
	if m.pane == paneModeTerminal && m.preview.IsAttached() {
		paneContent = m.preview.View()
		paneContent = lipgloss.NewStyle().Width(pw).Height(contentH).Render(paneContent)
	} else {
		paneContent = m.renderInfoPane(pw, contentH)
	}

	// Separator
	var sepLines []string
	for i := 0; i < contentH; i++ {
		sepLines = append(sepLines, "│")
	}
	separator := lipgloss.NewStyle().Foreground(m.theme.CardBorder.GetForeground()).
		Render(strings.Join(sepLines, "\n"))

	middle := lipgloss.JoinHorizontal(lipgloss.Top, listContent, separator, paneContent)

	// Full-width shortcuts and footer
	shortcuts := m.renderShortcuts()
	footer := m.renderFooter()

	base := lipgloss.JoinVertical(lipgloss.Left, header, resourceBar, middle, shortcuts, footer)

	// Modal overlays
	if m.dialog.Kind != DialogNone && !m.dialog.IsInlineDialog() && !m.dialog.IsContentDialog() && !m.dialog.IsSidePanel() {
		dialog := m.dialog.View()
		base = m.overlay(base, dialog)
	}

	if m.help.Visible() {
		helpView := m.help.View()
		base = m.overlay(base, helpView)
	}

	v := tea.NewView(base)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

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

	baseLines := strings.Split(base, "\n")
	dialogLines := strings.Split(dialog, "\n")

	for i, dl := range dialogLines {
		row := y + i
		if row >= len(baseLines) {
			break
		}
		line := baseLines[row]
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
