package tui

import "github.com/charmbracelet/lipgloss"

// HelpOverlay renders the help screen.
type HelpOverlay struct {
	visible bool
	theme   Theme
	width   int
	height  int
}

// NewHelpOverlay creates a new help overlay.
func NewHelpOverlay(theme Theme) HelpOverlay {
	return HelpOverlay{theme: theme}
}

// Toggle shows/hides help.
func (h *HelpOverlay) Toggle() {
	h.visible = !h.visible
}

// Visible returns whether help is shown.
func (h *HelpOverlay) Visible() bool {
	return h.visible
}

// Hide hides the help overlay.
func (h *HelpOverlay) Hide() {
	h.visible = false
}

// SetSize updates available dimensions.
func (h *HelpOverlay) SetSize(w, ht int) {
	h.width = w
	h.height = ht
}

// View renders the help overlay.
func (h *HelpOverlay) View() string {
	if !h.visible {
		return ""
	}

	title := h.theme.Bold.Render("CLAUDES — Key Bindings")

	bindings := []struct{ key, desc string }{
		{"n", "New instance"},
		{"d", "Toggle dangerous mode"},
		{"Enter", "Attach (tiled if multi-select)"},
		{"Ctrl+Enter", "Context menu"},
		{"Ctrl+s", "Stop instance(s)"},
		{"Ctrl+x", "Stop all idle instances"},
		{"Ctrl+d", "Delete instance(s) (confirm)"},
		{"Ctrl+r", "Resume instance"},
		{"Space", "Toggle multi-select"},
		{"Ctrl+a", "Select all"},
		{"Ctrl+g", "Group selected instances"},
		{"Ctrl+b", "Ungroup / break tiled view"},
		{"/", "Filter instances"},
		{"L", "Load profile"},
		{"Ctrl+←/→", "Prev/next window"},
		{"Ctrl+h/j/k/l", "Navigate panes"},
		{"j/k ↑/↓", "Navigate"},
		{"?", "Toggle this help"},
		{"q", "Quit"},
		{"", ""},
		{"Left-click", "Attach instance"},
		{"Right-click", "Context menu"},
	}

	var rows []string
	for _, b := range bindings {
		k := h.theme.Label.Width(12).Render(b.key)
		d := h.theme.Muted.Render(b.desc)
		rows = append(rows, k+d)
	}

	hint := h.theme.Muted.Render("\nPress ? or Esc to close")

	content := lipgloss.JoinVertical(lipgloss.Left,
		append([]string{title, ""}, append(rows, hint)...)...,
	)

	w := 45
	if w > h.width-4 {
		w = h.width - 4
	}

	return h.theme.Help.Width(w).Render(content)
}
