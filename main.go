package main

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/arlintdev/claudes/internal/claude"
	"github.com/arlintdev/claudes/internal/config"
	"github.com/arlintdev/claudes/internal/daemon"
	"github.com/arlintdev/claudes/internal/instance"
	"github.com/arlintdev/claudes/internal/store"
	"github.com/arlintdev/claudes/internal/tui"
	tea "charm.land/bubbletea/v2"
)

// Version is set at build time via -ldflags "-X main.Version=vX.Y.Z".
var Version = "dev"

func main() {
	// Subcommand: claudes daemon / claudes d
	if len(os.Args) > 1 && (os.Args[1] == "daemon" || os.Args[1] == "d") {
		if err := daemon.Run(); err != nil {
			fatal("%v", err)
		}
		return
	}

	checkPrereqs()

	cfg := config.Load()
	_ = config.EnsureDir()

	// Ensure daemon is running
	exe, err := os.Executable()
	if err != nil {
		fatal("cannot find executable: %v", err)
	}
	if err := daemon.EnsureRunning(exe); err != nil {
		fatal("daemon: %v", err)
	}

	// Connect to daemon
	client, err := daemon.Dial(daemon.SocketPath())
	if err != nil {
		fatal("connect to daemon: %v", err)
	}
	defer client.Close()

	runTUI(cfg, client)
}

func runTUI(cfg config.Config, dc *daemon.Client) {
	// Open SQLite store for persistence. nil = graceful degradation.
	var instStore instance.Store
	if db, err := store.Open(); err == nil {
		instStore = db
		defer db.Close()
	}

	mgr := instance.NewManager(instStore)
	launcher := claude.NewLauncher(dc, mgr)
	if launcher.Activity != nil {
		defer launcher.Activity.Close()
	}
	launcher.Reconcile()

	sessions := claude.NewSessionStore()
	sessions.ForceScan()

	model := tui.New(cfg, mgr, dc, launcher, sessions, Version)
	p := tea.NewProgram(model)

	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error running claudes: %v\n", err)
		os.Exit(1)
	}
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "Error: "+format+"\n", args...)
	os.Exit(1)
}

func checkPrereqs() {
	if _, err := exec.LookPath("claude"); err != nil {
		fmt.Fprintln(os.Stderr, "Missing required dependencies:")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "  ✗ Claude Code CLI")
		fmt.Fprintln(os.Stderr, "    Install: npm install -g @anthropic-ai/claude-code")
		os.Exit(1)
	}
}
