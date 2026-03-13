package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/arlintdev/claudes/internal/claude"
	"github.com/arlintdev/claudes/internal/sshconfig"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// DialogKind represents the type of dialog currently shown.
type DialogKind int

const (
	DialogNone DialogKind = iota
	DialogNew
	DialogConfirmDelete
	DialogConfirmBatchDelete
	DialogFilter
	DialogSessionPicker
	DialogGroupName
	DialogUpdate
)

// Model choices for the selector.
var modelChoices = []string{"", "sonnet", "opus", "haiku"}
var modelLabels = []string{"default", "sonnet", "opus", "haiku"}

// Dialog manages modal dialog state.
type Dialog struct {
	Kind       DialogKind
	inputs     []textinput.Model // for new instance dialog (name, dir, task)
	focus      int               // active input index (0-2 = text, 3 = model)
	target     string            // instance ID for confirm dialogs
	targetName string            // display name for confirm dialogs
	theme     Theme
	width     int
	height    int

	// New instance: model selector + toggles
	modelCur  int  // index into modelChoices
	dangerous bool // start in dangerous mode
	resume    bool // open with --resume (session picker)

	// New instance: host selector + docker image input
	hostChoices  []string         // e.g. ["local", "ssh:myhost", "docker"]
	hostCur      int              // index into hostChoices
	dockerImage  textinput.Model  // text input for docker image name

	// New instance: directory completions
	dirCompletions []string
	dirCompIdx     int // -1 = not completing

	// Batch delete targets
	batchTargets []string

	// Filter
	filterInput textinput.Model

	// Session picker
	sessions   []claude.SessionInfo
	sessionCur int

	// Group name dialog
	groupNameInput textinput.Model
	groupTargetIDs []string // IDs to group

	// Update dialog
	updateCurrent    string // current version
	updateAvailable  string // available version
	updateURL        string // download URL
	updateCur        int    // 0 = Update, 1 = Skip
	updateDownloading bool  // true while downloading
	updateErr        error  // download error
}

// NewDialog creates a new dialog handler.
func NewDialog(theme Theme) Dialog {
	fi := textinput.New()
	fi.Placeholder = "filter..."
	fi.CharLimit = 40

	return Dialog{
		Kind:        DialogNone,
		theme:       theme,
		filterInput: fi,
		dirCompIdx:  -1,
	}
}

// totalFields is the number of focusable fields in the new-instance form.
const totalFields = 8 // name, dir, task, model, host, dockerImage, danger, resume

// OpenNew opens the new instance side panel.
func (d *Dialog) OpenNew() {
	d.Kind = DialogNew
	d.focus = 0
	d.modelCur = 0
	d.dangerous = false
	d.resume = false
	d.dirCompletions = nil
	d.dirCompIdx = -1
	d.inputs = make([]textinput.Model, 3)

	d.inputs[0] = textinput.New()
	d.inputs[0].Placeholder = "instance name"
	d.inputs[0].CharLimit = 30
	d.inputs[0].Focus()

	d.inputs[1] = textinput.New()
	d.inputs[1].Placeholder = "~/path/to/project"
	d.inputs[1].CharLimit = 200

	d.inputs[2] = textinput.New()
	d.inputs[2].Placeholder = "initial task (optional)"
	d.inputs[2].CharLimit = 500

	// Build host choices: local + SSH hosts + docker
	d.hostChoices = []string{"local"}
	for _, h := range sshconfig.ParseHosts() {
		d.hostChoices = append(d.hostChoices, "ssh:"+h)
	}
	d.hostChoices = append(d.hostChoices, "docker")
	d.hostCur = 0

	d.dockerImage = textinput.New()
	d.dockerImage.Placeholder = "image:tag"
	d.dockerImage.CharLimit = 200
	d.dockerImage.SetValue(defaultDockerImage)
}

// OpenConfirmDelete opens the inline delete confirmation in the footer.
func (d *Dialog) OpenConfirmDelete(id, displayName string) {
	d.Kind = DialogConfirmDelete
	d.target = id
	d.targetName = displayName
}

// OpenConfirmBatchDelete opens the inline batch delete confirmation in the footer.
func (d *Dialog) OpenConfirmBatchDelete(names []string) {
	d.Kind = DialogConfirmBatchDelete
	d.batchTargets = names
}

// BatchTargets returns the batch delete target names.
func (d *Dialog) BatchTargets() []string {
	return d.batchTargets
}

// IsInlineDialog returns true if the current dialog renders in the footer, not as an overlay.
func (d *Dialog) IsInlineDialog() bool {
	return d.Kind == DialogFilter || d.Kind == DialogConfirmDelete || d.Kind == DialogConfirmBatchDelete || d.Kind == DialogGroupName
}

// IsContentDialog returns true if the dialog renders below the list, not as an overlay.
func (d *Dialog) IsContentDialog() bool {
	return d.Kind == DialogNew
}

// IsSidePanel returns true if the dialog renders as a side panel.
func (d *Dialog) IsSidePanel() bool {
	return false
}

// OpenFilter opens the filter input.
func (d *Dialog) OpenFilter() {
	d.Kind = DialogFilter
	d.filterInput.SetValue("")
	d.filterInput.Focus()
}

// OpenSessionPicker opens the session picker for targeted resume.
func (d *Dialog) OpenSessionPicker(id string, sessions []claude.SessionInfo) {
	d.Kind = DialogSessionPicker
	d.target = id
	d.sessions = sessions
	d.sessionCur = 0
}

// OpenGroupName opens the group naming dialog.
func (d *Dialog) OpenGroupName(selectedIDs []string) {
	d.Kind = DialogGroupName
	d.groupTargetIDs = selectedIDs
	d.groupNameInput = textinput.New()
	d.groupNameInput.Placeholder = "group name"
	d.groupNameInput.CharLimit = 30
	d.groupNameInput.SetValue(fmt.Sprintf("group-%d", len(selectedIDs)))
	d.groupNameInput.Focus()
	d.groupNameInput.CursorEnd()
}

// GroupNameValue returns the entered group name.
func (d *Dialog) GroupNameValue() string {
	return d.groupNameInput.Value()
}

// GroupTargetIDs returns the IDs to be grouped.
func (d *Dialog) GroupTargetIDs() []string {
	return d.groupTargetIDs
}

// OpenUpdate opens the update available dialog.
func (d *Dialog) OpenUpdate(current, available, downloadURL string) {
	d.Kind = DialogUpdate
	d.updateCurrent = current
	d.updateAvailable = available
	d.updateURL = downloadURL
	d.updateCur = 0
	d.updateDownloading = false
	d.updateErr = nil
}

// Close dismisses the dialog.
func (d *Dialog) Close() {
	d.Kind = DialogNone
	d.target = ""
	d.targetName = ""
	d.inputs = nil
	d.sessions = nil
	d.batchTargets = nil
	d.dirCompletions = nil
	d.dirCompIdx = -1
	d.groupTargetIDs = nil
	d.updateURL = ""
	d.updateDownloading = false
	d.updateErr = nil
}

// Target returns the target instance name (for confirm dialogs).
func (d *Dialog) Target() string {
	return d.target
}

// NewInstanceValues returns values from the new dialog inputs.
func (d *Dialog) NewInstanceValues() (name, dir, task, model, host string, dangerous, resume bool) {
	if len(d.inputs) < 3 {
		return
	}
	host = d.hostChoices[d.hostCur]
	if host == "docker" {
		img := d.dockerImage.Value()
		if img != "" {
			host = "docker:" + img
		}
	}
	return d.inputs[0].Value(), d.inputs[1].Value(), d.inputs[2].Value(), modelChoices[d.modelCur], host, d.dangerous, d.resume
}

// FilterValue returns the current filter text.
func (d *Dialog) FilterValue() string {
	return d.filterInput.Value()
}

// SelectedSession returns the selected session info, or nil.
func (d *Dialog) SelectedSession() *claude.SessionInfo {
	if d.sessionCur >= 0 && d.sessionCur < len(d.sessions) {
		return &d.sessions[d.sessionCur]
	}
	return nil
}

// Update handles input for the active dialog.
func (d *Dialog) Update(msg tea.Msg) tea.Cmd {
	switch d.Kind {
	case DialogNew:
		return d.updateNew(msg)
	case DialogFilter:
		return d.updateFilter(msg)
	case DialogSessionPicker:
		return d.updateSessionPicker(msg)
	case DialogGroupName:
		return d.updateGroupName(msg)
	case DialogUpdate:
		return d.updateUpdateDialog(msg)
	}
	return nil
}

func (d *Dialog) updateUpdateDialog(msg tea.Msg) tea.Cmd {
	if d.updateDownloading {
		return nil // ignore input while downloading
	}
	if msg, ok := msg.(tea.KeyPressMsg); ok {
		switch msg.String() {
		case "left", "h":
			if d.updateCur > 0 {
				d.updateCur--
			}
		case "right", "l":
			if d.updateCur < 1 {
				d.updateCur++
			}
		case "tab":
			d.updateCur = (d.updateCur + 1) % 2
		}
	}
	return nil
}

func (d *Dialog) updateNew(msg tea.Msg) tea.Cmd {
	if msg, ok := msg.(tea.KeyPressMsg); ok {
		switch {
		case key.Matches(msg, key.NewBinding(key.WithKeys("tab"))):
			d.advanceFocus(1)
			return nil
		case key.Matches(msg, key.NewBinding(key.WithKeys("shift+tab"))):
			d.advanceFocus(-1)
			return nil
		}

		// Model selector: left/right/space when focused on model field
		if d.focus == 3 {
			switch msg.String() {
			case "left", "h":
				if d.modelCur > 0 {
					d.modelCur--
				}
			case "right", "l", "space":
				if d.modelCur < len(modelChoices)-1 {
					d.modelCur++
				} else {
					d.modelCur = 0 // wrap around on space
				}
			}
			return nil
		}

		// Host selector: left/right/space when focused on host field
		if d.focus == 4 {
			switch msg.String() {
			case "left", "h":
				if d.hostCur > 0 {
					d.hostCur--
				}
			case "right", "l", "space":
				if d.hostCur < len(d.hostChoices)-1 {
					d.hostCur++
				} else {
					d.hostCur = 0 // wrap around on space
				}
			}
			return nil
		}

		// Docker image text input
		if d.focus == 5 {
			var cmd tea.Cmd
			d.dockerImage, cmd = d.dockerImage.Update(msg)
			return cmd
		}

		// Danger toggle: space/left/right to toggle
		if d.focus == 6 {
			switch msg.String() {
			case "space", "left", "right", "h", "l":
				d.dangerous = !d.dangerous
			}
			return nil
		}

		// Resume toggle: space/left/right to toggle
		if d.focus == 7 {
			switch msg.String() {
			case "space", "left", "right", "h", "l":
				d.resume = !d.resume
			}
			return nil
		}

		// Directory field: update completions on every keystroke
		if d.focus == 1 {
			defer d.updateDirCompletions()
		}
	}

	// Only update text inputs when focus is on a text field (0-2)
	if d.focus < 3 {
		var cmd tea.Cmd
		d.inputs[d.focus], cmd = d.inputs[d.focus].Update(msg)
		return cmd
	}
	return nil
}

func (d *Dialog) advanceFocus(delta int) {
	if d.focus < 3 {
		d.inputs[d.focus].Blur()
	}
	if d.focus == 5 {
		d.dockerImage.Blur()
	}
	d.focus = (d.focus + delta + totalFields) % totalFields
	// Skip dockerImage field (5) when host is not "docker"
	if d.focus == 5 && d.hostChoices[d.hostCur] != "docker" {
		d.focus = (d.focus + delta + totalFields) % totalFields
	}
	if d.focus < 3 {
		d.inputs[d.focus].Focus()
	}
	if d.focus == 5 {
		d.dockerImage.Focus()
	}
	d.dirCompletions = nil
	d.dirCompIdx = -1
}

// updateDirCompletions refreshes the directory completion list based on current input.
func (d *Dialog) updateDirCompletions() {
	raw := d.inputs[1].Value()
	if raw == "" {
		d.dirCompletions = nil
		d.dirCompIdx = -1
		return
	}

	path := expandHome(raw)

	dir := path
	prefix := ""
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		dir = filepath.Dir(path)
		prefix = filepath.Base(path)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		d.dirCompletions = nil
		d.dirCompIdx = -1
		return
	}

	var matches []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue // skip hidden dirs
		}
		if prefix == "" || strings.HasPrefix(strings.ToLower(name), strings.ToLower(prefix)) {
			full := filepath.Join(dir, name)
			matches = append(matches, contractHome(full))
		}
	}
	sort.Strings(matches)

	if len(matches) > 8 {
		matches = matches[:8]
	}

	d.dirCompletions = matches
	d.dirCompIdx = -1
}

// AcceptCompletion fills in the selected directory completion.
func (d *Dialog) AcceptCompletion(idx int) {
	if idx >= 0 && idx < len(d.dirCompletions) {
		d.inputs[1].SetValue(d.dirCompletions[idx] + "/")
		d.inputs[1].CursorEnd()
		d.dirCompletions = nil
		d.dirCompIdx = -1
	}
}

func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") || path == "~" {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, path[1:])
	}
	return path
}

// defaultDockerImage is the default image pre-filled for docker instances.
const defaultDockerImage = "node:20"

func contractHome(path string) string {
	home, _ := os.UserHomeDir()
	if strings.HasPrefix(path, home) {
		return "~" + path[len(home):]
	}
	return path
}

func (d *Dialog) updateFilter(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	d.filterInput, cmd = d.filterInput.Update(msg)
	return cmd
}

func (d *Dialog) updateSessionPicker(msg tea.Msg) tea.Cmd {
	if msg, ok := msg.(tea.KeyPressMsg); ok {
		switch msg.String() {
		case "j", "down":
			if d.sessionCur < len(d.sessions)-1 {
				d.sessionCur++
			}
		case "k", "up":
			if d.sessionCur > 0 {
				d.sessionCur--
			}
		}
	}
	return nil
}

func (d *Dialog) updateGroupName(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	d.groupNameInput, cmd = d.groupNameInput.Update(msg)
	return cmd
}

// SetWidth sets the dialog render width.
func (d *Dialog) SetWidth(w int) {
	d.width = w
}

// SetHeight sets the dialog render height (for side panel).
func (d *Dialog) SetHeight(h int) {
	d.height = h
}

// View renders the dialog.
func (d *Dialog) View() string {
	switch d.Kind {
	case DialogNew:
		return d.viewNew()
	case DialogConfirmDelete:
		return d.viewConfirmDelete()
	case DialogFilter:
		return d.viewFilter()
	case DialogConfirmBatchDelete:
		return d.viewConfirmBatchDelete()
	case DialogSessionPicker:
		return d.viewSessionPicker()
	case DialogGroupName:
		return d.viewGroupName()
	case DialogUpdate:
		return d.viewUpdate()
	default:
		return ""
	}
}

func (d *Dialog) viewNew() string {
	cardW := d.width
	if cardW < 30 {
		cardW = 30
	}
	innerW := cardW - 4 // border + padding

	line1 := d.fieldLabel("Name", 0) + d.inputs[0].View()
	line2 := d.fieldLabel("Dir", 1) + d.inputs[1].View()
	line3 := d.fieldLabel("Task", 2) + d.inputs[2].View()
	line4 := d.fieldLabel("Model", 3) + d.renderSelector(3, modelLabels, d.modelCur)

	hostLabels := make([]string, len(d.hostChoices))
	for i, h := range d.hostChoices {
		if strings.HasPrefix(h, "ssh:") {
			hostLabels[i] = h[4:]
		} else {
			hostLabels[i] = h
		}
	}
	line5 := d.fieldLabel("Host", 4) + d.renderSelector(4, hostLabels, d.hostCur)
	line6 := d.renderToggle("danger", d.dangerous, 6) + "  " + d.renderToggle("resume", d.resume, 7)

	var hint string
	switch {
	case d.focus < 3 || d.focus == 5:
		hint = d.theme.Muted.Render("Tab: next  Enter: create  Esc: cancel")
	case d.focus == 3 || d.focus == 4:
		hint = d.theme.Muted.Render("◀▶/Space: select  Tab: next  Enter: create")
	case d.focus == 6 || d.focus == 7:
		hint = d.theme.Muted.Render("Space/◀▶: toggle  Tab: next  Enter: create")
	}
	hintLine := "  " + hint

	var rows []string
	rows = append(rows, line1, line2, line3)

	if d.focus == 1 && len(d.dirCompletions) > 0 {
		for j, comp := range d.dirCompletions {
			prefix := "  "
			if j == d.dirCompIdx {
				prefix = "▸ "
			}
			rows = append(rows, d.theme.Muted.Render("         "+prefix+comp))
		}
	}

	rows = append(rows, line4, line5)

	if d.hostChoices[d.hostCur] == "docker" {
		imgLine := d.fieldLabel("Image", 5) + d.dockerImage.View()
		rows = append(rows, imgLine)
	}

	rows = append(rows, line6, hintLine)

	content := strings.Join(rows, "\n")

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(d.theme.CardBorderSelected.GetForeground()).
		Width(innerW).
		Padding(0, 1).
		Render(content)
}

func (d *Dialog) renderSelector(focusIdx int, labels []string, cur int) string {
	focused := d.focus == focusIdx
	if focused {
		var parts []string
		for i, label := range labels {
			if i == cur {
				parts = append(parts, d.theme.Bold.Render("["+label+"]"))
			} else {
				parts = append(parts, d.theme.Muted.Render(" "+label+" "))
			}
		}
		return "◀ " + strings.Join(parts, "") + " ▶"
	}
	return d.theme.Muted.Render(labels[cur])
}

func (d *Dialog) renderToggle(label string, on bool, focusIdx int) string {
	focused := d.focus == focusIdx
	check := "[ ]"
	if on {
		check = "[x]"
	}

	if focused {
		accent := d.theme.Bold
		if label == "danger" && on {
			accent = d.theme.ModeDanger
		}
		return accent.Render("◀ " + check + " " + label + " ▶")
	}
	if on && label == "danger" {
		return d.theme.ModeDanger.Render(check + " " + label)
	}
	if on {
		return d.theme.Bold.Render(check + " " + label)
	}
	return d.theme.Muted.Render(check + " " + label)
}

func (d *Dialog) fieldLabel(name string, focusIdx int) string {
	padded := fmt.Sprintf("%-6s", name+":")
	if d.focus == focusIdx {
		return d.theme.Bold.Render("▸ " + padded)
	}
	return d.theme.Label.Render("  " + padded)
}

func (d *Dialog) viewGroupName() string {
	return d.theme.Label.Render("Group name: ") + d.groupNameInput.View() +
		"  " + d.theme.Muted.Render("Enter confirm  Esc cancel")
}

func (d *Dialog) viewConfirmDelete() string {
	name := d.targetName
	if name == "" {
		name = d.target
	}
	return d.theme.ModeDanger.Render("Delete "+name+"? ") +
		d.theme.Muted.Render("(y/enter) confirm  (n/esc) cancel")
}

func (d *Dialog) viewConfirmBatchDelete() string {
	return d.theme.ModeDanger.Render(fmt.Sprintf("Delete %d instances? ", len(d.batchTargets))) +
		d.theme.Muted.Render("(y/enter) confirm  (n/esc) cancel")
}

func (d *Dialog) viewFilter() string {
	return d.theme.Label.Render("/") + d.filterInput.View()
}

func (d *Dialog) viewSessionPicker() string {
	title := d.theme.Bold.Render("Resume Session")

	if len(d.sessions) == 0 {
		msg := d.theme.Muted.Render("No sessions found for this directory.")
		hint := d.theme.Muted.Render("Esc: cancel")
		content := lipgloss.JoinVertical(lipgloss.Left, title, "", msg, "", hint)
		return d.theme.Dialog.Width(55).Render(content)
	}

	var items []string
	for i, sess := range d.sessions {
		date := sess.Modified.Format("Jan 02")
		summary := sess.Summary
		if len(summary) > 38 {
			summary = summary[:35] + "..."
		}
		tokens := fmt.Sprintf("%s/%s",
			claude.FormatTokens(sess.TokensIn),
			claude.FormatTokens(sess.TokensOut),
		)

		line := fmt.Sprintf("%-7s %-38s %s", date, summary, tokens)

		prefix := "  "
		if i == d.sessionCur {
			prefix = "▸ "
			items = append(items, d.theme.Bold.Render(prefix+line))
		} else {
			items = append(items, d.theme.Muted.Render(prefix+line))
		}
	}

	hint := d.theme.Muted.Render("Enter: resume  Esc: cancel  j/k: navigate")

	content := lipgloss.JoinVertical(lipgloss.Left,
		append([]string{title, ""}, append(items, "", hint)...)...,
	)

	w := d.width / 2
	if w < 60 {
		w = 60
	}
	return d.theme.Dialog.Width(w).Render(content)
}

func (d *Dialog) viewUpdate() string {
	title := d.theme.Bold.Render("Update Available")

	version := fmt.Sprintf("%s → %s", d.updateCurrent, d.updateAvailable)

	if d.updateDownloading {
		content := lipgloss.JoinVertical(lipgloss.Left,
			title, "", version, "",
			d.theme.Bold.Render("Downloading..."),
		)
		return d.theme.Dialog.Width(44).Render(content)
	}

	if d.updateErr != nil {
		content := lipgloss.JoinVertical(lipgloss.Left,
			title, "", version, "",
			d.theme.ErrorFlash.Render("Error: "+d.updateErr.Error()), "",
			d.theme.Muted.Render("Esc: close"),
		)
		return d.theme.Dialog.Width(44).Render(content)
	}

	options := []string{"Update & Restart", "Skip"}
	var parts []string
	for i, opt := range options {
		if i == d.updateCur {
			parts = append(parts, d.theme.Bold.
				Background(d.theme.Selected.GetBackground()).
				Padding(0, 2).
				Render(opt))
		} else {
			parts = append(parts, d.theme.Muted.Padding(0, 2).Render(opt))
		}
	}
	buttons := lipgloss.JoinHorizontal(lipgloss.Center, parts...)

	hint := d.theme.Muted.Render("←/→ select  Enter confirm  Esc skip")

	content := lipgloss.JoinVertical(lipgloss.Center,
		title, "", version, "", buttons, "", hint,
	)
	return d.theme.Dialog.Width(44).Render(content)
}
