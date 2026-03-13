package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/arlintdev/claudes/internal/claude"
	"github.com/arlintdev/claudes/internal/instance"
	"charm.land/lipgloss/v2"
)

// ListView renders the instance list as bordered cards.
type ListView struct {
	instances       []*instance.Instance
	cursor          int
	width           int
	height          int
	scrollOffset    int // first visible row (in visual row space)
	theme           Theme
	filter          string
	activity        map[string]string              // id → last activity line
	activityStates  map[string]*claude.ActivityState // id → enriched state
	selected        map[string]bool                 // multi-select set (id → true)
	animFrame       int                             // animation frame counter for robots
	previewFocused  bool                            // true when preview pane has focus
}

const cardRows = 5 // top border (with title+status) + 3 content lines + bottom border

const robotWidth = 6 // each robot pose is 6 chars wide

// Tick advances the animation frame counter.
func (l *ListView) Tick() {
	l.animFrame++
}

// robotFrames defines animation frames for each activity/status.
// Each entry is a slice of [3]string frames that cycle. All frames are 6 chars wide.
var robotFrames = map[string][][3]string{
	"thinking": {
		{` [..] `, ` -[]- `, `  ╨╨  `},
		{` [. ] `, ` -[]- `, `  ╨╨  `},
		{` [..] `, ` -[]- `, `  ╨╨  `},
		{` [ .] `, ` -[]- `, `  ╨╨  `},
	},
	"reading": {
		{` [°°] `, ` -[]-╗`, `  ╨╨  `},
		{` [°°] `, ` -[]- `, `  ╨╨  `},
		{` [°°] `, `╔-[]- `, `  ╨╨  `},
		{` [°°] `, ` -[]- `, `  ╨╨  `},
	},
	"writing": {
		{` [..] `, ` -[]╗ `, `  ╨╨  `},
		{` [..] `, ` -[]-╗`, `  ╨╨  `},
		{` [..] `, ` -[]╝ `, `  ╨╨  `},
		{` [..] `, ` -[]-╝`, `  ╨╨  `},
	},
	"running": {
		{` \../ `, ` -[]- `, ` ╨  ╨ `},
		{` [..] `, `/-[]-\`, `  ╨╨  `},
		{` \../ `, ` -[]- `, ` ╨  ╨ `},
		{` [..] `, `\-[]-/`, `  ╨╨  `},
	},
	"searching": {
		{` [¬¬] `, ` -[]- `, `  ╨╨  `},
		{` [¬ ] `, ` -[]- `, ` ╨ ╨  `},
		{` [¬¬] `, ` -[]- `, `  ╨╨  `},
		{` [ ¬] `, ` -[]- `, `  ╨ ╨ `},
	},
	"browsing": {
		{` [..] `, `╔-[]- `, `  ╨╨  `},
		{` [oo] `, ` -[]- `, `  ╨╨  `},
		{` [..] `, ` -[]-╗`, `  ╨╨  `},
		{` [oo] `, ` -[]- `, `  ╨╨  `},
	},
	"spawning": {
		{` [..] `, ` -[]-+`, `  ╨╨  `},
		{` [..] `, ` -[]- `, `  ╨╨ .`},
		{` [..] `, ` -[]- `, `  ╨╨  `},
		{` [..] `, ` -[]-+`, `  ╨╨  `},
	},
	"waiting": {
		{` [..] `, `  []  `, `  ╨╨  `},
		{` [  ] `, `  []  `, `  ╨╨  `},
		{` [..] `, `  []  `, `  ╨╨  `},
		{` [  ] `, `  []  `, `  ╨╨  `},
	},
	"idle": {
		{` [..] `, `  []° `, `  ╨╨  `},
		{` [..] `, `  []°°`, `  ╨╨  `},
		{` [--] `, `  []  `, `  ╨╨  `},
		{` [..] `, `  []° `, `  ╨╨  `},
	},
	"error": {
		{`![!!]!`, `\-[]-/`, ` ╨  ╨ `},
		{` [!!] `, `/-[]-\`, `  ╨╨  `},
		{`![!!]!`, `\-[]-/`, ` ╨  ╨ `},
		{` [!!] `, `/-[]-\`, `  ╨╨  `},
	},
	"stopped": {
		{` [__] `, `  []  `, `  ╨╨  `},
	},
}

// robotFrame returns the current animation frame for a given robot key.
func (l *ListView) robotFrame(key string) [3]string {
	frames := robotFrames[key]
	if len(frames) == 0 {
		frames = robotFrames["stopped"]
	}
	return frames[l.animFrame%len(frames)]
}

// NewListView creates a new list view.
func NewListView(theme Theme) ListView {
	return ListView{
		theme:          theme,
		activity:       make(map[string]string),
		activityStates: make(map[string]*claude.ActivityState),
		selected:       make(map[string]bool),
	}
}

// SetActivityState stores the enriched activity state for an instance.
func (l *ListView) SetActivityState(id string, state *claude.ActivityState) {
	l.activityStates[id] = state
}

// ActivityState returns the activity state for an instance.
func (l *ListView) ActivityState(id string) *claude.ActivityState {
	return l.activityStates[id]
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
	// Remember which instance the cursor is on (by ID) so we can follow it after re-sort
	var cursorID string
	if l.cursor >= 0 && l.cursor < len(l.instances) {
		cursorID = l.instances[l.cursor].ID
	}

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

	// Sort: active before stopped, then ungrouped before grouped, then by group, then by name
	sort.SliceStable(l.instances, func(i, j int) bool {
		ai := l.instances[i].Status != instance.StatusStopped && l.instances[i].Status != instance.StatusError
		aj := l.instances[j].Status != instance.StatusStopped && l.instances[j].Status != instance.StatusError
		if ai != aj {
			return ai // active first
		}
		gi, gj := l.instances[i].GroupName, l.instances[j].GroupName
		if (gi == "") != (gj == "") {
			return gi == "" // ungrouped first
		}
		if gi != gj {
			return gi < gj
		}
		return l.instances[i].Name < l.instances[j].Name
	})

	// Follow cursor by instance ID after re-sort
	if cursorID != "" {
		for i, inst := range l.instances {
			if inst.ID == cursorID {
				l.cursor = i
				break
			}
		}
	}
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
	if len(l.instances) == 0 {
		return
	}
	l.cursor--
	if l.cursor < 0 {
		l.cursor = len(l.instances) - 1
	}
}

// MoveDown moves the cursor down.
func (l *ListView) MoveDown() {
	if len(l.instances) == 0 {
		return
	}
	l.cursor++
	if l.cursor >= len(l.instances) {
		l.cursor = 0
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

// ScrollOffset returns the current scroll offset for mouse click adjustment.
func (l *ListView) ScrollOffset() int {
	return l.scrollOffset
}

// InstanceAtRow maps a visual row (0-based, relative to first card row,
// already adjusted for scroll offset) to an instance index. Returns -1 if the
// row lands on a group separator, card border, or is out of range.
func (l *ListView) InstanceAtRow(row int) int {
	if row < 0 || len(l.instances) == 0 {
		return -1
	}
	// Account for scroll offset
	row += l.scrollOffset

	currentRow := 0
	lastGroup := ""
	shownStoppedSep := false
	for i, inst := range l.instances {
		// Stopped separator
		isStopped := inst.Status == instance.StatusStopped || inst.Status == instance.StatusError
		if isStopped && !shownStoppedSep {
			shownStoppedSep = true
			if currentRow == row {
				return -1
			}
			currentRow++
		}
		if inst.GroupName != "" && inst.GroupName != lastGroup {
			// Group separator = 1 row
			if currentRow == row {
				return -1
			}
			currentRow++
		}
		lastGroup = inst.GroupName
		// Card = cardRows visual rows
		if row >= currentRow && row < currentRow+cardRows {
			return i
		}
		currentRow += cardRows
	}
	return -1
}

// View renders the card-based instance list.
func (l *ListView) View() string {
	if len(l.instances) == 0 {
		return l.theme.Muted.Render("  No instances. Press 'n' to create one.")
	}

	// Card dimensions
	cardW := l.width
	if cardW < 20 {
		cardW = 20
	}
	fullW := cardW - 2  // total card visual width (matches old lipgloss border output)
	contentW := fullW - 4 // text area inside │ + padding

	// Build all visual rows and track which row the cursor card starts at
	var allRows []string
	cursorStartRow := 0
	lastGroup := ""
	shownStoppedSep := false

	for i, inst := range l.instances {
		// Separator between active and stopped instances
		isStopped := inst.Status == instance.StatusStopped || inst.Status == instance.StatusError
		if isStopped && !shownStoppedSep {
			shownStoppedSep = true
			label := " ── stopped "
			pad := cardW - lipgloss.Width(label) - 1
			if pad < 0 {
				pad = 0
			}
			sep := l.theme.Muted.Render(label + strings.Repeat("─", pad))
			allRows = append(allRows, sep)
		}

		// Group separator
		if inst.GroupName != "" && inst.GroupName != lastGroup {
			label := " ── " + inst.GroupName + " "
			pad := cardW - lipgloss.Width(label) - 1
			if pad < 0 {
				pad = 0
			}
			sep := l.theme.GroupSeparator.Render(label + strings.Repeat("─", pad))
			allRows = append(allRows, sep)
		}
		lastGroup = inst.GroupName

		if i == l.cursor {
			cursorStartRow = len(allRows)
		}

		// Pick border color
		isCursor := i == l.cursor
		isMultiSel := l.selected[inst.ID]
		var borderStyle lipgloss.Style
		switch {
		case isCursor:
			borderStyle = l.theme.CardBorderSelected
		case isMultiSel:
			borderStyle = l.theme.CardBorderMultiSelected
		default:
			borderStyle = l.theme.CardBorder
		}
		bc := lipgloss.NewStyle().Foreground(borderStyle.GetForeground())
		rc := bc

		// Top border: ╭─ name ──── ● Status  5m ─╮
		dot, statusText, age, rKey, rStyle := l.statusInfo(inst)
		nameStr := l.theme.Bold.Render(truncate(inst.Name, contentW/3))

		leftPart := bc.Render("╭─ ") + nameStr + bc.Render(" ")
		rightPart := dot + " " + statusText + "  " + l.theme.Muted.Render(age) + " " + rc.Render("─╮")
		fillW := fullW - lipgloss.Width(leftPart) - lipgloss.Width(rightPart)
		if fillW < 0 {
			fillW = 0
		}
		topBorder := leftPart + bc.Render(strings.Repeat("─", fillW)) + rightPart

		// Content lines (3 lines) with robot on the right
		vBarL := bc.Render("│")
		vBarR := rc.Render("│")
		pose := l.robotFrame(rKey)
		poseStyle := rStyle
		textW := contentW - robotWidth - 1 // leave room for robot + gap
		contentLines := []string{
			l.cardLine2(inst, textW),
			l.cardLine3(inst, textW),
			l.cardLine4(inst, textW),
		}
		var wrappedLines []string
		for j, line := range contentLines {
			padW := textW - lipgloss.Width(line)
			if padW < 0 {
				padW = 0
			}
			inner := " " + line + strings.Repeat(" ", padW) + " " + poseStyle.Render(pose[j]) + " "
			wrappedLines = append(wrappedLines, vBarL+inner+vBarR)
		}

		// Bottom border
		bottomBorder := bc.Render("╰"+strings.Repeat("─", fullW-2)) + rc.Render("╯")

		// Assemble card
		cardParts := []string{topBorder}
		cardParts = append(cardParts, wrappedLines...)
		cardParts = append(cardParts, bottomBorder)
		card := strings.Join(cardParts, "\n")

		// Cursor indicator: left side when in menu, right side when preview focused
		indicator := l.theme.CardBorderSelected.Render("▐")
		cardLines := strings.Split(card, "\n")
		if isCursor && l.previewFocused {
			// Right-side indicator
			for j := range cardLines {
				cardLines[j] = " " + cardLines[j] + indicator
			}
		} else if isCursor {
			// Left-side indicator
			for j := range cardLines {
				cardLines[j] = indicator + cardLines[j]
			}
		} else {
			for j := range cardLines {
				cardLines[j] = " " + cardLines[j]
			}
		}
		card = strings.Join(cardLines, "\n")

		allRows = append(allRows, card)
	}

	// Scroll offset: keep cursor card visible
	l.adjustScroll(cursorStartRow, allRows)

	// Slice visible rows
	totalRows := countVisualRows(allRows)
	if l.scrollOffset > totalRows-l.height {
		l.scrollOffset = max(0, totalRows-l.height)
	}

	visible := sliceVisualRows(allRows, l.scrollOffset, l.height)
	return strings.Join(visible, "\n")
}

// statusInfo returns the colored dot, status label, age, and robot key for an instance.
func (l *ListView) statusInfo(inst *instance.Instance) (dot, status, age, robotKey string, robotStyle lipgloss.Style) {
	age = inst.Uptime()
	state := l.activityStates[inst.ID]
	switch {
	case inst.Status == instance.StatusIdle:
		dot = l.theme.StatusIdle.Render("●")
		status = l.theme.StatusIdle.Render("idle")
		robotKey, robotStyle = "idle", l.theme.StatusIdle
	case inst.Status == instance.StatusRunning:
		if state != nil && state.Kind != claude.ActivityNone {
			kindStyle := l.activityKindStyle(state.Kind)
			dot = kindStyle.Render("●")
			status = kindStyle.Render(state.Kind.String())
			robotStyle = kindStyle
			switch state.Kind {
			case claude.ActivityThinking:
				robotKey = "thinking"
			case claude.ActivityReading:
				robotKey = "reading"
			case claude.ActivityWriting:
				robotKey = "writing"
			case claude.ActivityRunning:
				robotKey = "running"
			case claude.ActivitySearching:
				robotKey = "searching"
			case claude.ActivityBrowsing:
				robotKey = "browsing"
			case claude.ActivitySpawning:
				robotKey = "spawning"
			case claude.ActivityWaiting:
				robotKey = "waiting"
			default:
				robotKey = "running"
			}
		} else {
			dot = l.theme.StatusIdle.Render("●")
			status = l.theme.StatusIdle.Render("idle")
			robotKey, robotStyle = "idle", l.theme.StatusIdle
		}
	case inst.Status == instance.StatusError:
		dot = l.StatusDot(inst.Status)
		status = l.StyleStatus(inst.Status)
		robotKey, robotStyle = "error", l.theme.StatusError
	default:
		dot = l.StatusDot(inst.Status)
		status = l.StyleStatus(inst.Status)
		robotKey, robotStyle = "stopped", l.theme.StatusStopped
	}
	return
}

// cardLine2: mode + dir
func (l *ListView) cardLine2(inst *instance.Instance, w int) string {
	mode := l.StyleMode(inst.Mode)
	dir := truncate(inst.Dir, w-lipgloss.Width(mode)-3)
	return mode + "  " + l.theme.Muted.Render(dir)
}

// cardLine3: activity sparkline
func (l *ListView) cardLine3(inst *instance.Instance, w int) string {
	state := l.activityStates[inst.ID]
	if state == nil || len(state.Timestamps) == 0 {
		return l.theme.Muted.Render(strings.Repeat("─", w-2))
	}
	spark := claude.RenderSparkline(state.Timestamps, claude.SparklineWindow, w-2)
	// Color the sparkline with the activity kind color
	kind := claude.ActivityThinking
	if state.Kind != claude.ActivityNone {
		kind = state.Kind
	}
	return l.activityKindStyle(kind).Render(spark)
}

// cardLine4: model + cost + tokens
func (l *ListView) cardLine4(inst *instance.Instance, w int) string {
	state := l.activityStates[inst.ID]
	if state == nil || (state.Model == "" && state.CostUSD == 0 && state.TokensIn == 0) {
		return l.theme.Muted.Render("-")
	}

	var parts []string
	if state.Model != "" {
		parts = append(parts, l.theme.Bold.Render(state.Model))
	}
	if state.CostUSD > 0 {
		parts = append(parts, l.theme.Muted.Render(fmt.Sprintf("$%.2f", state.CostUSD)))
	}
	if state.TokensIn > 0 || state.TokensOut > 0 {
		parts = append(parts, l.theme.Muted.Render(
			formatTokens(state.TokensIn)+"↑ "+formatTokens(state.TokensOut)+"↓"))
	}

	line := strings.Join(parts, "  ")
	return truncate(line, w-2)
}

// formatTokens formats a token count with k/M suffixes.
func formatTokens(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// activityKindStyle returns the style for a given activity kind.
func (l *ListView) activityKindStyle(k claude.ActivityKind) lipgloss.Style {
	switch k {
	case claude.ActivityReading:
		return l.theme.ActivityReading
	case claude.ActivityWriting:
		return l.theme.ActivityWriting
	case claude.ActivityRunning:
		return l.theme.ActivityRunning
	case claude.ActivitySearching:
		return l.theme.ActivitySearching
	case claude.ActivityBrowsing:
		return l.theme.ActivityBrowsing
	case claude.ActivitySpawning:
		return l.theme.ActivitySpawning
	case claude.ActivityThinking:
		return l.theme.ActivityThinking
	case claude.ActivityWaiting:
		return l.theme.ActivityWaiting
	default:
		return l.theme.StatusRunning
	}
}

// StatusDot returns a colored bullet for the status.
func (l *ListView) StatusDot(s instance.Status) string {
	dot := "●"
	switch s {
	case instance.StatusRunning:
		return l.theme.StatusRunning.Render(dot)
	case instance.StatusIdle:
		return l.theme.StatusIdle.Render(dot)
	case instance.StatusError:
		return l.theme.StatusError.Render(dot)
	default:
		return l.theme.StatusStopped.Render(dot)
	}
}

// adjustScroll ensures the cursor card is visible within l.height.
func (l *ListView) adjustScroll(cursorStartRow int, allRows []string) {
	if l.height <= 0 {
		return
	}
	// Convert cursorStartRow (index into allRows) to visual row offset
	visualRow := 0
	for i := 0; i < cursorStartRow && i < len(allRows); i++ {
		visualRow += strings.Count(allRows[i], "\n") + 1
	}
	cursorEndRow := visualRow + cardRows

	// Scroll up if cursor is above viewport
	if visualRow < l.scrollOffset {
		l.scrollOffset = visualRow
	}
	// Scroll down if cursor is below viewport
	if cursorEndRow > l.scrollOffset+l.height {
		l.scrollOffset = cursorEndRow - l.height
	}
}

// countVisualRows counts total visual rows in the rendered output.
func countVisualRows(rows []string) int {
	total := 0
	for _, r := range rows {
		total += strings.Count(r, "\n") + 1
	}
	return total
}

// sliceVisualRows returns visible lines from offset up to limit lines.
func sliceVisualRows(rows []string, offset, limit int) []string {
	var allLines []string
	for _, r := range rows {
		allLines = append(allLines, strings.Split(r, "\n")...)
	}
	if offset >= len(allLines) {
		return nil
	}
	end := offset + limit
	if end > len(allLines) {
		end = len(allLines)
	}
	return allLines[offset:end]
}

func (l *ListView) StyleStatus(s instance.Status) string {
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

func (l *ListView) StyleMode(m instance.Mode) string {
	if m == instance.ModeDanger {
		return l.theme.ModeDanger.Render(m.String())
	}
	return l.theme.ModeSafe.Render(m.String())
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
