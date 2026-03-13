package tui

import "charm.land/lipgloss/v2"

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
		{"m", "Toggle mode (safe/danger)"},
		{"s", "Stop instance(s)"},
		{"x", "Stop all idle instances"},
		{"d", "Delete instance(s) (confirm)"},
		{"g", "Group / ungroup"},
		{"Enter", "Attach instance (focus preview)"},
		{"Ctrl+Space", "Detach (return to menu)"},
		{"Ctrl+r", "Resume instance"},
		{"Space", "Toggle multi-select"},
		{"Ctrl+a", "Select all"},
		{"/", "Filter instances"},
		{"j/k ↑/↓", "Navigate"},
		{"?", "Toggle this help"},
		{"q", "Quit"},
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
