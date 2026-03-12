package tui

import "github.com/charmbracelet/bubbles/key"

// KeyMap defines all key bindings for the app.
type KeyMap struct {
	Up        key.Binding
	Down      key.Binding
	New       key.Binding
	Danger    key.Binding
	Stop      key.Binding
	Delete    key.Binding
	Resume    key.Binding
	Attach    key.Binding
	Filter    key.Binding
	Profile   key.Binding
	Help      key.Binding
	Quit      key.Binding
	Confirm   key.Binding
	Cancel    key.Binding
	Tab       key.Binding
	Select    key.Binding
	SelectAll key.Binding
	Group     key.Binding
	BreakTile key.Binding
	TileGroup key.Binding
	StopIdle  key.Binding
}

// DefaultKeyMap returns the default key bindings (k9s-style).
func DefaultKeyMap() KeyMap {
	return KeyMap{
		Up: key.NewBinding(
			key.WithKeys("k", "up"),
			key.WithHelp("↑/k", "up"),
		),
		Down: key.NewBinding(
			key.WithKeys("j", "down"),
			key.WithHelp("↓/j", "down"),
		),
		New: key.NewBinding(
			key.WithKeys("n"),
			key.WithHelp("n", "new instance"),
		),
		Danger: key.NewBinding(
			key.WithKeys("d"),
			key.WithHelp("d", "toggle danger mode"),
		),
		Stop: key.NewBinding(
			key.WithKeys("ctrl+s"),
			key.WithHelp("ctrl+s", "stop instance"),
		),
		Delete: key.NewBinding(
			key.WithKeys("ctrl+d"),
			key.WithHelp("ctrl+d", "delete instance"),
		),
		Resume: key.NewBinding(
			key.WithKeys("ctrl+r"),
			key.WithHelp("ctrl+r", "resume instance"),
		),
		Attach: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("enter", "attach/resume"),
		),
		Filter: key.NewBinding(
			key.WithKeys("/"),
			key.WithHelp("/", "filter"),
		),
		Profile: key.NewBinding(
			key.WithKeys("L"),
			key.WithHelp("L", "load profile"),
		),
		Help: key.NewBinding(
			key.WithKeys("?"),
			key.WithHelp("?", "help"),
		),
		Quit: key.NewBinding(
			key.WithKeys("q", "ctrl+c"),
			key.WithHelp("q/ctrl+c", "quit"),
		),
		Confirm: key.NewBinding(
			key.WithKeys("y", "enter"),
			key.WithHelp("y/enter", "confirm"),
		),
		Cancel: key.NewBinding(
			key.WithKeys("n", "esc"),
			key.WithHelp("n/esc", "cancel"),
		),
		Tab: key.NewBinding(
			key.WithKeys("tab"),
			key.WithHelp("tab", "next field"),
		),
		Select: key.NewBinding(
			key.WithKeys(" "),
			key.WithHelp("Space", "toggle select"),
		),
		SelectAll: key.NewBinding(
			key.WithKeys("ctrl+a"),
			key.WithHelp("ctrl+a", "select all"),
		),
		Group: key.NewBinding(
			key.WithKeys("ctrl+g"),
			key.WithHelp("ctrl+g", "group selected"),
		),
		BreakTile: key.NewBinding(
			key.WithKeys("ctrl+b"),
			key.WithHelp("ctrl+b", "ungroup/break tile"),
		),
		TileGroup: key.NewBinding(
			key.WithKeys("t"),
			key.WithHelp("t", "open group tiled"),
		),
		StopIdle: key.NewBinding(
			key.WithKeys("ctrl+x"),
			key.WithHelp("ctrl+x", "stop all idle"),
		),
	}
}
