package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/arlintdev/claudes/internal/claude"
	"github.com/arlintdev/claudes/internal/instance"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// DialogKind represents the type of dialog currently shown.
type DialogKind int

const (
	DialogNone DialogKind = iota
	DialogNew
	DialogConfirmDelete
	DialogConfirmBatchDelete
	DialogProfile
	DialogFilter
	DialogSessionPicker
	DialogContextMenu
	DialogGroupName
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
	profiles   []string          // available profile names
	profCur   int               // profile cursor
	theme     Theme
	width     int
	height    int

	// New instance: model selector + toggles
	modelCur  int  // index into modelChoices
	dangerous bool // start in dangerous mode
	resume    bool // open with --resume (session picker)

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

	// Context menu
	menuItems []menuItem
	menuCur   int
	menuX     int
	menuY     int

	// Group name dialog
	groupNameInput textinput.Model
	groupTargetIDs []string // IDs to group
}

// menuItem represents a single context menu entry.
type menuItem struct {
	label  string
	action string
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
const totalFields = 6 // name, dir, task, model, danger, resume

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

// IsSidePanel returns true if the dialog renders as a side panel.
func (d *Dialog) IsSidePanel() bool {
	return d.Kind == DialogNew
}

// OpenProfile opens the profile picker.
func (d *Dialog) OpenProfile(profiles []string) {
	d.Kind = DialogProfile
	d.profiles = profiles
	d.profCur = 0
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

// OpenContextMenu opens a right-click context menu for an instance.
func (d *Dialog) OpenContextMenu(inst *instance.Instance, x, y int) {
	d.Kind = DialogContextMenu
	d.target = inst.ID
	d.targetName = inst.Name
	d.menuCur = 0
	d.menuX = x
	d.menuY = y

	var items []menuItem
	switch inst.Status {
	case instance.StatusStopped, instance.StatusError:
		items = append(items, menuItem{"Resume", "resume"})
	default:
		items = append(items, menuItem{"Attach", "attach"})
		items = append(items, menuItem{"Stop", "stop"})
	}
	items = append(items, menuItem{"Toggle Danger", "danger"})
	items = append(items, menuItem{"Delete", "delete"})
	if inst.GroupName != "" {
		items = append(items, menuItem{"Ungroup", "ungroup"})
	}
	d.menuItems = items
}

// SelectedMenuAction returns the action string of the currently highlighted menu item.
func (d *Dialog) SelectedMenuAction() string {
	if d.menuCur >= 0 && d.menuCur < len(d.menuItems) {
		return d.menuItems[d.menuCur].action
	}
	return ""
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
	d.menuItems = nil
	d.groupTargetIDs = nil
}

// Target returns the target instance name (for confirm dialogs).
func (d *Dialog) Target() string {
	return d.target
}

// NewInstanceValues returns values from the new dialog inputs.
func (d *Dialog) NewInstanceValues() (name, dir, task, model string, dangerous, resume bool) {
	if len(d.inputs) < 3 {
		return
	}
	return d.inputs[0].Value(), d.inputs[1].Value(), d.inputs[2].Value(), modelChoices[d.modelCur], d.dangerous, d.resume
}

// FilterValue returns the current filter text.
func (d *Dialog) FilterValue() string {
	return d.filterInput.Value()
}

// SelectedProfile returns the name of the selected profile.
func (d *Dialog) SelectedProfile() string {
	if d.profCur >= 0 && d.profCur < len(d.profiles) {
		return d.profiles[d.profCur]
	}
	return ""
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
	case DialogProfile:
		return d.updateProfile(msg)
	case DialogSessionPicker:
		return d.updateSessionPicker(msg)
	case DialogContextMenu:
		return d.updateContextMenu(msg)
	case DialogGroupName:
		return d.updateGroupName(msg)
	}
	return nil
}

func (d *Dialog) updateNew(msg tea.Msg) tea.Cmd {
	if msg, ok := msg.(tea.KeyMsg); ok {
		switch {
		case key.Matches(msg, key.NewBinding(key.WithKeys("tab"))):
			d.advanceFocus(1)
			return nil
		case key.Matches(msg, key.NewBinding(key.WithKeys("shift+tab"))):
			d.advanceFocus(-1)
			return nil
		}

		// Model selector: left/right when focused on model field
		if d.focus == 3 {
			switch msg.String() {
			case "left", "h":
				if d.modelCur > 0 {
					d.modelCur--
				}
			case "right", "l":
				if d.modelCur < len(modelChoices)-1 {
					d.modelCur++
				}
			}
			return nil
		}

		// Danger toggle: space to toggle
		if d.focus == 4 {
			if msg.String() == " " {
				d.dangerous = !d.dangerous
			}
			return nil
		}

		// Resume toggle: space to toggle
		if d.focus == 5 {
			if msg.String() == " " {
				d.resume = !d.resume
			}
			return nil
		}

		// Directory field: tab-completion on Tab is handled by advanceFocus,
		// but we handle the actual autocomplete trigger here.
		if d.focus == 1 {
			switch msg.String() {
			case "ctrl+i": // ctrl+i = tab in some terminals, but we use tab for field nav
				// no-op, handled above
			}
			// Update completions on every keystroke in dir field
			defer d.updateDirCompletions()
		}
	}

	// Only update text inputs when focus is on a text field
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
	d.focus = (d.focus + delta + totalFields) % totalFields
	if d.focus < 3 {
		d.inputs[d.focus].Focus()
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

	// If path exists and is a directory, list its children
	// If not, treat it as a partial name and list parent's children matching prefix
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

	// Limit to 8 suggestions
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

func (d *Dialog) updateProfile(msg tea.Msg) tea.Cmd {
	if msg, ok := msg.(tea.KeyMsg); ok {
		switch msg.String() {
		case "j", "down":
			if d.profCur < len(d.profiles)-1 {
				d.profCur++
			}
		case "k", "up":
			if d.profCur > 0 {
				d.profCur--
			}
		}
	}
	return nil
}

func (d *Dialog) updateSessionPicker(msg tea.Msg) tea.Cmd {
	if msg, ok := msg.(tea.KeyMsg); ok {
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

func (d *Dialog) updateContextMenu(msg tea.Msg) tea.Cmd {
	if msg, ok := msg.(tea.KeyMsg); ok {
		switch msg.String() {
		case "j", "down":
			if d.menuCur < len(d.menuItems)-1 {
				d.menuCur++
			}
		case "k", "up":
			if d.menuCur > 0 {
				d.menuCur--
			}
		}
	}
	return nil
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
	case DialogProfile:
		return d.viewProfile()
	case DialogFilter:
		return d.viewFilter()
	case DialogConfirmBatchDelete:
		return d.viewConfirmBatchDelete()
	case DialogSessionPicker:
		return d.viewSessionPicker()
	case DialogContextMenu:
		return d.viewContextMenu()
	case DialogGroupName:
		return d.viewGroupName()
	default:
		return ""
	}
}

func (d *Dialog) viewNew() string {
	w := d.width
	if w < 30 {
		w = 30
	}
	innerW := w - 4 // border + padding

	title := d.theme.Bold.Render("New Instance")

	// Field labels and inputs
	type field struct {
		label string
		view  string
	}

	fields := []field{
		{"Name", d.inputs[0].View()},
		{"Dir", d.inputs[1].View()},
		{"Task", d.inputs[2].View()},
	}

	var rows []string
	for i, f := range fields {
		label := d.theme.Label.Render(f.label)
		if d.focus == i {
			label = d.theme.Bold.Render(f.label)
		}
		rows = append(rows, label)
		rows = append(rows, f.view)

		// Show directory completions below the Dir field
		if i == 1 && d.focus == 1 && len(d.dirCompletions) > 0 {
			for j, comp := range d.dirCompletions {
				prefix := "  "
				if j == d.dirCompIdx {
					prefix = "▸ "
				}
				styled := d.theme.Muted.Render(prefix + comp)
				rows = append(rows, styled)
			}
		}

		rows = append(rows, "") // spacer
	}

	// Model selector
	modelLabel := d.theme.Label.Render("Model")
	if d.focus == 3 {
		modelLabel = d.theme.Bold.Render("Model")
	}
	rows = append(rows, modelLabel)

	var modelParts []string
	for i, label := range modelLabels {
		if i == d.modelCur {
			modelParts = append(modelParts, d.theme.Bold.
				Background(d.theme.Selected.GetBackground()).
				Padding(0, 1).
				Render(label))
		} else {
			modelParts = append(modelParts, d.theme.Muted.Padding(0, 1).Render(label))
		}
	}
	rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Center, modelParts...))
	rows = append(rows, "")

	// Danger toggle
	dangerLabel := d.theme.Label.Render("Mode")
	if d.focus == 4 {
		dangerLabel = d.theme.Bold.Render("Mode")
	}
	rows = append(rows, dangerLabel)
	if d.dangerous {
		rows = append(rows, d.theme.ModeDanger.Render("[x] DANGEROUS"))
	} else {
		rows = append(rows, d.theme.ModeSafe.Render("[ ] safe"))
	}
	rows = append(rows, "")

	// Resume toggle
	resumeLabel := d.theme.Label.Render("Resume")
	if d.focus == 5 {
		resumeLabel = d.theme.Bold.Render("Resume")
	}
	rows = append(rows, resumeLabel)
	if d.resume {
		rows = append(rows, d.theme.Bold.Render("[x] resume previous session"))
	} else {
		rows = append(rows, d.theme.Muted.Render("[ ] new session"))
	}
	rows = append(rows, "")

	// Hints
	hint := d.theme.Muted.Render("Tab next  Enter create  Esc cancel")
	switch d.focus {
	case 3:
		hint = d.theme.Muted.Render("←/→ select  Tab next  Enter create")
	case 4, 5:
		hint = d.theme.Muted.Render("Space toggle  Tab next  Enter create")
	}
	rows = append(rows, hint)

	content := lipgloss.JoinVertical(lipgloss.Left, rows...)

	return d.theme.SidePanel.
		Width(innerW).
		Height(d.height - 2).
		Render(lipgloss.JoinVertical(lipgloss.Left, title, "", content))
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

func (d *Dialog) viewProfile() string {
	title := d.theme.Bold.Render("Load Profile")
	if len(d.profiles) == 0 {
		msg := d.theme.Muted.Render("No profiles found in ~/.config/claudes/profiles/")
		hint := d.theme.Muted.Render("Esc: close")
		content := lipgloss.JoinVertical(lipgloss.Left, title, "", msg, "", hint)
		return d.theme.Dialog.Width(50).Render(content)
	}

	var items []string
	for i, p := range d.profiles {
		prefix := "  "
		if i == d.profCur {
			prefix = "▸ "
			items = append(items, d.theme.Bold.Render(prefix+p))
		} else {
			items = append(items, d.theme.Muted.Render(prefix+p))
		}
	}
	hint := d.theme.Muted.Render("Enter: load  Esc: cancel")

	content := lipgloss.JoinVertical(lipgloss.Left,
		append([]string{title, ""}, append(items, "", hint)...)...,
	)
	return d.theme.Dialog.Width(40).Render(content)
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

func (d *Dialog) viewContextMenu() string {
	name := d.targetName
	if name == "" {
		name = d.target
	}
	title := d.theme.Bold.Render(name)

	var rows []string
	for i, item := range d.menuItems {
		prefix := "  "
		if i == d.menuCur {
			prefix = "▸ "
			rows = append(rows, d.theme.Bold.Render(prefix+item.label))
		} else {
			rows = append(rows, d.theme.Muted.Render(prefix+item.label))
		}
	}

	content := lipgloss.JoinVertical(lipgloss.Left,
		append([]string{title, ""}, rows...)...,
	)
	return d.theme.Dialog.Width(24).Render(content)
}
