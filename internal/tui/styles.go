package tui

import "charm.land/lipgloss/v2"

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

	CardBorder              lipgloss.Style
	CardBorderSelected      lipgloss.Style
	CardBorderMultiSelected lipgloss.Style

	// Activity kind colors (used for status dot + label when running)
	ActivityReading   lipgloss.Style
	ActivityWriting   lipgloss.Style
	ActivityRunning   lipgloss.Style
	ActivitySearching lipgloss.Style
	ActivityBrowsing  lipgloss.Style
	ActivitySpawning  lipgloss.Style
	ActivityThinking  lipgloss.Style
	ActivityWaiting   lipgloss.Style
}

// DefaultTheme returns the k9s-inspired color theme.
func DefaultTheme() Theme {
	accent := lipgloss.Color("#DA7756") // Claude Code terracotta
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
			Foreground(accent).
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
			BorderForeground(accent).
			Padding(1, 2),
		Help: lipgloss.NewStyle().
			Border(lipgloss.DoubleBorder()).
			BorderForeground(accent).
			Padding(1, 2),

		StatusRunning: lipgloss.NewStyle().Foreground(green),
		StatusIdle:    lipgloss.NewStyle().Foreground(yellow),
		StatusError:   lipgloss.NewStyle().Foreground(red),
		StatusStopped: lipgloss.NewStyle().Foreground(gray),
		ModeDanger:    lipgloss.NewStyle().Bold(true).Foreground(red),
		ModeSafe:      lipgloss.NewStyle().Foreground(green),

		Label: lipgloss.NewStyle().Foreground(accent),
		Muted: lipgloss.NewStyle().Foreground(gray),
		Bold:  lipgloss.NewStyle().Bold(true).Foreground(white),

		// K9s-style elements
		ShortcutKey:  lipgloss.NewStyle().Bold(true).Foreground(yellow),
		ShortcutDesc: lipgloss.NewStyle().Foreground(dimWhite),
		Logo:         lipgloss.NewStyle().Bold(true).Foreground(accent),
		HeaderLabel:  lipgloss.NewStyle().Bold(true).Foreground(accent),
		HeaderValue:  lipgloss.NewStyle().Foreground(dimWhite),
		HeaderInfo:   lipgloss.NewStyle().Foreground(accent),
		ErrorFlash:     lipgloss.NewStyle().Bold(true).Foreground(red).Padding(0, 1),
		GroupSeparator: lipgloss.NewStyle().Bold(true).Foreground(accent),

		CardBorder:              lipgloss.NewStyle().Foreground(gray),
		CardBorderSelected:      lipgloss.NewStyle().Foreground(accent),
		CardBorderMultiSelected: lipgloss.NewStyle().Foreground(lipgloss.Color("#00FFFF")),

		ActivityReading:   lipgloss.NewStyle().Foreground(lipgloss.Color("#38BDF8")),
		ActivityWriting:   lipgloss.NewStyle().Foreground(lipgloss.Color("#FB923C")),
		ActivityRunning:   lipgloss.NewStyle().Foreground(lipgloss.Color("#A78BFA")),
		ActivitySearching: lipgloss.NewStyle().Foreground(lipgloss.Color("#34D399")),
		ActivityBrowsing:  lipgloss.NewStyle().Foreground(lipgloss.Color("#22D3EE")),
		ActivitySpawning:  lipgloss.NewStyle().Foreground(lipgloss.Color("#F472B6")),
		ActivityThinking:  lipgloss.NewStyle().Foreground(dimWhite),
		ActivityWaiting:   lipgloss.NewStyle().Foreground(yellow),
	}
}
