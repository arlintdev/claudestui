package tui

import "github.com/charmbracelet/lipgloss"

// Theme holds all styled components.
type Theme struct {
	Header      lipgloss.Style
	Footer      lipgloss.Style
	TableHeader lipgloss.Style
	TableRow    lipgloss.Style
	Selected      lipgloss.Style
	MultiSelected lipgloss.Style
	SidePanel     lipgloss.Style
	Border      lipgloss.Style
	Dialog      lipgloss.Style
	Help        lipgloss.Style

	StatusRunning lipgloss.Style
	StatusIdle    lipgloss.Style
	StatusError   lipgloss.Style
	StatusStopped lipgloss.Style
	ModeDanger    lipgloss.Style
	ModeSafe      lipgloss.Style

	Label lipgloss.Style
	Muted lipgloss.Style
	Bold  lipgloss.Style

	// K9s-style elements
	ShortcutKey  lipgloss.Style
	ShortcutDesc lipgloss.Style
	Logo         lipgloss.Style
	HeaderLabel  lipgloss.Style // left-side label (e.g., "Instances:")
	HeaderValue  lipgloss.Style // left-side value
	HeaderInfo     lipgloss.Style
	ErrorFlash     lipgloss.Style
	GroupSeparator lipgloss.Style
}

// DefaultTheme returns the k9s-inspired color theme.
func DefaultTheme() Theme {
	cyan := lipgloss.Color("#00D7FF")
	green := lipgloss.Color("#00ff87")
	red := lipgloss.Color("#ff005f")
	yellow := lipgloss.Color("#FFD700")
	gray := lipgloss.Color("#666666")
	white := lipgloss.Color("#ffffff")
	dimWhite := lipgloss.Color("#AAAAAA")
	selectedBg := lipgloss.Color("#3465A4")      // GNOME blue
	multiSelBg := lipgloss.Color("#2E4057") // darker blue for multi-select

	return Theme{
		Header: lipgloss.NewStyle().
			Bold(true).
			Foreground(cyan).
			Padding(0, 1),
		Footer: lipgloss.NewStyle().
			Foreground(dimWhite).
			Padding(0, 1),
		TableHeader: lipgloss.NewStyle().
			Bold(true).
			Foreground(white).
			BorderBottom(true).
			BorderStyle(lipgloss.NormalBorder()).
			BorderForeground(gray),
		TableRow: lipgloss.NewStyle().
			Foreground(dimWhite).
			Padding(0, 1),
		Selected: lipgloss.NewStyle().
			Bold(true).
			Foreground(white).
			Background(selectedBg).
			Padding(0, 1),
		MultiSelected: lipgloss.NewStyle().
			Foreground(white).
			Background(multiSelBg).
			Padding(0, 1),
		SidePanel: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(gray).
			Padding(0, 1),
		Border: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(gray),
		Dialog: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(cyan).
			Padding(1, 2),
		Help: lipgloss.NewStyle().
			Border(lipgloss.DoubleBorder()).
			BorderForeground(cyan).
			Padding(1, 2),

		StatusRunning: lipgloss.NewStyle().Foreground(green),
		StatusIdle:    lipgloss.NewStyle().Foreground(yellow),
		StatusError:   lipgloss.NewStyle().Foreground(red),
		StatusStopped: lipgloss.NewStyle().Foreground(gray),
		ModeDanger:    lipgloss.NewStyle().Bold(true).Foreground(red),
		ModeSafe:      lipgloss.NewStyle().Foreground(green),

		Label: lipgloss.NewStyle().Foreground(cyan),
		Muted: lipgloss.NewStyle().Foreground(gray),
		Bold:  lipgloss.NewStyle().Bold(true).Foreground(white),

		// K9s-style elements
		ShortcutKey:  lipgloss.NewStyle().Bold(true).Foreground(yellow),
		ShortcutDesc: lipgloss.NewStyle().Foreground(dimWhite),
		Logo:         lipgloss.NewStyle().Bold(true).Foreground(cyan),
		HeaderLabel:  lipgloss.NewStyle().Bold(true).Foreground(cyan),
		HeaderValue:  lipgloss.NewStyle().Foreground(dimWhite),
		HeaderInfo:   lipgloss.NewStyle().Foreground(cyan),
		ErrorFlash:     lipgloss.NewStyle().Bold(true).Foreground(red).Padding(0, 1),
		GroupSeparator: lipgloss.NewStyle().Bold(true).Foreground(cyan),
	}
}
