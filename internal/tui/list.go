package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/arlintdev/claudes/internal/instance"
	"github.com/charmbracelet/lipgloss"
)

// ListView renders the instance table.
type ListView struct {
	instances []*instance.Instance
	cursor    int
	width     int
	height    int
	theme     Theme
	filter    string
	activity  map[string]string // id → last activity line
	selected  map[string]bool   // multi-select set (id → true)
}

// NewListView creates a new list view.
func NewListView(theme Theme) ListView {
	return ListView{theme: theme, activity: make(map[string]string), selected: make(map[string]bool)}
}

// SetActivity stores the last activity line for an instance (keyed by ID).
func (l *ListView) SetActivity(id, line string) {
	l.activity[id] = line
}

// SetSize updates the available dimensions.
func (l *ListView) SetSize(w, h int) {
	l.width = w
	l.height = h
}

// SetFilter sets the filter string.
func (l *ListView) SetFilter(f string) {
	l.filter = f
}

// Sync updates the instance list and cleans stale selections.
func (l *ListView) Sync(instances []*instance.Instance) {
	if l.filter != "" {
		var filtered []*instance.Instance
		for _, inst := range instances {
			if strings.Contains(strings.ToLower(inst.Name), strings.ToLower(l.filter)) ||
				strings.Contains(strings.ToLower(inst.Dir), strings.ToLower(l.filter)) {
				filtered = append(filtered, inst)
			}
		}
		l.instances = filtered
	} else {
		l.instances = instances
	}

	// Sort: ungrouped first (by name), then grouped by group name, then by name within group
	sort.SliceStable(l.instances, func(i, j int) bool {
		gi, gj := l.instances[i].GroupName, l.instances[j].GroupName
		if (gi == "") != (gj == "") {
			return gi == "" // ungrouped first
		}
		if gi != gj {
			return gi < gj
		}
		return l.instances[i].Name < l.instances[j].Name
	})

	if l.cursor >= len(l.instances) && len(l.instances) > 0 {
		l.cursor = len(l.instances) - 1
	}
	// Clean stale selections for removed instances
	visible := make(map[string]bool, len(l.instances))
	for _, inst := range l.instances {
		visible[inst.ID] = true
	}
	for id := range l.selected {
		if !visible[id] {
			delete(l.selected, id)
		}
	}
}

// MoveUp moves the cursor up.
func (l *ListView) MoveUp() {
	if l.cursor > 0 {
		l.cursor--
	}
}

// MoveDown moves the cursor down.
func (l *ListView) MoveDown() {
	if l.cursor < len(l.instances)-1 {
		l.cursor++
	}
}

// Selected returns the currently selected instance.
func (l *ListView) Selected() *instance.Instance {
	if l.cursor < 0 || l.cursor >= len(l.instances) {
		return nil
	}
	return l.instances[l.cursor]
}

// ToggleSelected toggles the item at the cursor in the multi-select set.
func (l *ListView) ToggleSelected() {
	if sel := l.Selected(); sel != nil {
		if l.selected[sel.ID] {
			delete(l.selected, sel.ID)
		} else {
			l.selected[sel.ID] = true
		}
	}
}

// SelectAll selects all visible instances.
func (l *ListView) SelectAll() {
	for _, inst := range l.instances {
		l.selected[inst.ID] = true
	}
}

// ClearSelected clears all multi-selections.
func (l *ListView) ClearSelected() {
	l.selected = make(map[string]bool)
}

// IsSelected returns whether an instance ID is multi-selected.
func (l *ListView) IsSelected(id string) bool {
	return l.selected[id]
}

// SelectedCount returns the number of multi-selected items.
func (l *ListView) SelectedCount() int {
	return len(l.selected)
}

// SelectedIDs returns the IDs of all multi-selected items, or nil if none.
func (l *ListView) SelectedIDs() []string {
	if len(l.selected) == 0 {
		return nil
	}
	ids := make([]string, 0, len(l.selected))
	for id := range l.selected {
		ids = append(ids, id)
	}
	return ids
}

// Cursor returns the current cursor index.
func (l *ListView) Cursor() int {
	return l.cursor
}

// SetCursor sets the cursor to a specific instance index (clamped to bounds).
func (l *ListView) SetCursor(idx int) {
	if idx < 0 {
		idx = 0
	}
	if idx >= len(l.instances) {
		idx = len(l.instances) - 1
	}
	if idx >= 0 {
		l.cursor = idx
	}
}

// InstanceAtRow maps a content row (0-based, relative to first data row after
// the table header) to an instance index. Returns -1 if the row lands on a
// group separator or is out of range. The table header itself is row -1 (not
// passed here).
func (l *ListView) InstanceAtRow(row int) int {
	if row < 0 || len(l.instances) == 0 {
		return -1
	}
	// Walk through instances, accounting for separator rows inserted before
	// the first member of each new group.
	currentRow := 0
	lastGroup := ""
	for i, inst := range l.instances {
		if inst.GroupName != "" && inst.GroupName != lastGroup {
			// This row is a separator
			if currentRow == row {
				return -1
			}
			currentRow++
		}
		lastGroup = inst.GroupName
		if currentRow == row {
			return i
		}
		currentRow++
	}
	return -1
}

// View renders the 7-column table.
func (l *ListView) View() string {
	if len(l.instances) == 0 {
		msg := l.theme.Muted.Render("  No instances. Press 'n' to create one.")
		return msg
	}

	hasSelection := len(l.selected) > 0
	checkW := 0
	if hasSelection {
		checkW = 4 // "[x] " or "[ ] "
	}

	// Column widths
	colName := 14
	colStatus := 10
	colMode := 8
	colHost := 12
	colCPU := 5
	colMem := 7
	colUp := 8
	fixedCols := checkW + colName + colStatus + colMode + colHost + colCPU + colMem + colUp + 8 // 8 for spacing
	remaining := l.width - fixedCols
	colDir := max(remaining*30/100, 12)
	colActivity := max(remaining-colDir-1, 10) // -1 for spacing

	// Header (plain strings, no ANSI — fmt.Sprintf works fine)
	checkHeader := ""
	if hasSelection {
		checkHeader = "    " // 4 chars to match checkbox column
	}
	header := fmt.Sprintf(" %s%-*s %-*s %-*s %-*s %-*s %*s %*s %-*s %-*s",
		checkHeader,
		colName, "NAME",
		colStatus, "STATUS",
		colMode, "MODE",
		colHost, "HOST",
		colDir, "DIR",
		colCPU, "CPU",
		colMem, "MEM",
		colUp, "AGE",
		colActivity, "ACTIVITY",
	)
	headerLine := l.theme.TableHeader.Width(l.width).Render(header)

	// Rows
	var rows []string
	rows = append(rows, headerLine)

	lastGroup := ""
	for i, inst := range l.instances {
		// Insert group separator before first instance of a new group
		if inst.GroupName != "" && inst.GroupName != lastGroup {
			label := " ── " + inst.GroupName + " "
			pad := l.width - lipgloss.Width(label) - 1
			if pad < 0 {
				pad = 0
			}
			sep := l.theme.GroupSeparator.Render(label + strings.Repeat("─", pad))
			rows = append(rows, sep)
		}
		lastGroup = inst.GroupName

		status := l.styleStatus(inst.Status)
		mode := l.styleMode(inst.Mode)
		dir := truncate(inst.Dir, colDir)
		act := truncate(l.activity[inst.ID], colActivity)
		if act == "" {
			act = "-"
		}

		cpu := "-"
		mem := "-"
		if inst.Status != instance.StatusStopped && inst.PanePID != "" {
			cpu = fmt.Sprintf("%.0f%%", inst.CPU)
			mem = formatMem(inst.MemKB)
		}

		// Checkbox prefix (only when multi-select is active)
		checkPrefix := ""
		if hasSelection {
			if l.selected[inst.ID] {
				checkPrefix = "[x] "
			} else {
				checkPrefix = "[ ] "
			}
		}

		hostLabel := truncate(inst.HostLabel(), colHost)

		// Use padRight for styled strings (status, mode) since ANSI codes
		// break fmt.Sprintf width calculations.
		row := " " + checkPrefix + padRight(truncate(inst.Name, colName), colName) + " " +
			padRight(status, colStatus) + " " +
			padRight(mode, colMode) + " " +
			padRight(hostLabel, colHost) + " " +
			padRight(dir, colDir) + " " +
			fmt.Sprintf("%*s", colCPU, cpu) + " " +
			fmt.Sprintf("%*s", colMem, mem) + " " +
			fmt.Sprintf("%-*s", colUp, inst.Uptime()) + " " +
			padRight(act, colActivity)

		// Three-tier styling: cursor > multi-selected > normal
		switch {
		case i == l.cursor:
			row = l.theme.Selected.Width(l.width).Render(row)
		case l.selected[inst.ID]:
			row = l.theme.MultiSelected.Width(l.width).Render(row)
		default:
			row = l.theme.TableRow.Width(l.width).Render(row)
		}
		rows = append(rows, row)
	}

	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}

func (l *ListView) styleStatus(s instance.Status) string {
	switch s {
	case instance.StatusRunning:
		return l.theme.StatusRunning.Render(s.String())
	case instance.StatusIdle:
		return l.theme.StatusIdle.Render(s.String())
	case instance.StatusError:
		return l.theme.StatusError.Render(s.String())
	default:
		return l.theme.StatusStopped.Render(s.String())
	}
}

func (l *ListView) styleMode(m instance.Mode) string {
	if m == instance.ModeDanger {
		return l.theme.ModeDanger.Render(m.String())
	}
	return l.theme.ModeSafe.Render(m.String())
}

// padRight pads a (possibly styled) string to targetWidth based on visible width.
func padRight(s string, targetWidth int) string {
	visible := lipgloss.Width(s)
	if visible >= targetWidth {
		return s
	}
	return s + strings.Repeat(" ", targetWidth-visible)
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return "…" + s[len(s)-(maxLen-1):]
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// formatMem formats RSS in KB to a human-readable string.
func formatMem(kb uint64) string {
	switch {
	case kb >= 1024*1024:
		return fmt.Sprintf("%.1fG", float64(kb)/(1024*1024))
	case kb >= 1024:
		return fmt.Sprintf("%.0fM", float64(kb)/1024)
	default:
		return fmt.Sprintf("%dK", kb)
	}
}
