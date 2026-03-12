package main

import (
	"fmt"
	"os"

	"github.com/arlintdev/claudes/internal/claude"
	"github.com/arlintdev/claudes/internal/config"
	"github.com/arlintdev/claudes/internal/instance"
	"github.com/arlintdev/claudes/internal/store"
	"github.com/arlintdev/claudes/internal/tmux"
	"github.com/arlintdev/claudes/internal/tui"
	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	cfg := config.Load()
	_ = config.EnsureDir()

	tc := tmux.NewClient()

	// If we're already inside the claudes tmux session, run the TUI.
	if tc.InsideClaudesSession() {
		runTUI(cfg, tc)
		return
	}

	// Bootstrap: get the user into the claudes tmux session.
	exe, err := os.Executable()
	if err != nil {
		fatal("cannot find executable: %v", err)
	}

	created, err := tc.EnsureSession(exe)
	if err != nil {
		fatal("%v\nMake sure tmux is installed and accessible.", err)
	}

	if !created {
		// Session already exists but we're outside it.
		// Respawn the dashboard window with claudes in case the TUI exited.
		_ = tc.RespawnDashboard(exe)
	}

	// Attach (or switch-client if already in a different tmux session).
	if os.Getenv("TMUX") == "" {
		if err := tc.Attach(); err != nil {
			fatal("attach: %v", err)
		}
	} else {
		if err := tc.SwitchClient(); err != nil {
			fatal("switch-client: %v", err)
		}
	}
}

func runTUI(cfg config.Config, tc *tmux.Client) {
	tc.SetupKeybindings()
	defer tc.CleanupKeybindings()

	// Open SQLite store for persistence. nil = graceful degradation.
	var instStore instance.Store
	if db, err := store.Open(); err == nil {
		instStore = db
		defer db.Close()
	}

	mgr := instance.NewManager(instStore)
	launcher := claude.NewLauncher(tc, mgr)
	if launcher.Activity != nil {
		defer launcher.Activity.Close()
	}
	launcher.Reconcile()

	sessions := claude.NewSessionStore()
	sessions.ForceScan()

	model := tui.New(cfg, mgr, tc, launcher, sessions)
	p := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion())

	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error running claudes: %v\n", err)
		os.Exit(1)
	}
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "Error: "+format+"\n", args...)
	os.Exit(1)
}
