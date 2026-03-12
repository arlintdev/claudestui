# Claudes TUI

A tmux-based terminal dashboard for managing multiple [Claude Code](https://docs.anthropic.com/en/docs/claude-code) instances. Launch, monitor, group, and switch between Claude sessions from a single pane of glass.

```
 Instances: 4 (2 running, 1 idle, 1 error)     ╔═╗╦  ╔═╗╦ ╦╔╦╗╔═╗╔═╗
 CPU:       23%                                  ║  ║  ╠═╣║ ║ ║║║╣ ╚═╗
 MEM:       8.2G/16.0G                           ╚═╝╩═╝╩ ╩╚═╝═╩╝╚═╝╚═╝
                                                     » made by ARLINTDEV
```

## Features

- **Multi-instance management** — Launch, stop, resume, and delete Claude Code instances
- **Real-time monitoring** — CPU, memory, token usage, and activity tracking per instance
- **Tiled pane views** — Group instances and view them side-by-side in tmux
- **Session persistence** — SQLite-backed state survives restarts; resume sessions without losing context
- **Mouse support** — Left-click to attach, right-click for context menu
- **Profile system** — Define instance presets in YAML for rapid multi-instance deployment
- **Safety controls** — Toggle between safe and dangerous (`--dangerously-skip-permissions`) modes
- **Activity watcher** — Parses Claude's JSONL output to detect idle/running state and show last action

## Prerequisites

| Dependency | Install |
|---|---|
| **tmux** | `brew install tmux` (macOS) / `apt install tmux` (Linux) |
| **Claude Code CLI** | `npm install -g @anthropic-ai/claude-code` |

The app checks for these on startup and tells you what's missing.

## Installation

### From release binaries

Download the latest binary from [Releases](https://github.com/arlintdev/claudestui/releases):

```bash
# macOS (Apple Silicon)
curl -L https://github.com/arlintdev/claudestui/releases/latest/download/panes-darwin-arm64 -o panes
chmod +x panes && sudo mv panes /usr/local/bin/

# macOS (Intel)
curl -L https://github.com/arlintdev/claudestui/releases/latest/download/panes-darwin-amd64 -o panes
chmod +x panes && sudo mv panes /usr/local/bin/

# Linux (amd64)
curl -L https://github.com/arlintdev/claudestui/releases/latest/download/panes-linux-amd64 -o panes
chmod +x panes && sudo mv panes /usr/local/bin/

# Linux (arm64)
curl -L https://github.com/arlintdev/claudestui/releases/latest/download/panes-linux-arm64 -o panes
chmod +x panes && sudo mv panes /usr/local/bin/
```

### From source

```bash
go install github.com/arlintdev/claudes@latest
```

## Usage

```bash
panes
```

On first run, the app creates a dedicated tmux session called `claudes` and drops you into the dashboard. If the session already exists, it reattaches.

### How it works

1. **Dashboard** — The main TUI runs in a tmux window called `dashboard`
2. **Instances** — Each Claude instance gets its own tmux window
3. **Navigation** — Switch between the dashboard and instances using keybindings
4. **Persistence** — Instance state is stored in `~/.config/claudes/claudes.db`

## Keybindings

### Dashboard

| Key | Action |
|---|---|
| `j` / `k` / `↑` / `↓` | Navigate instance list |
| `n` | New instance |
| `Enter` | Attach to instance (tiled if grouped) |
| `Ctrl+Enter` | Open context menu |
| `d` | Toggle dangerous mode |
| `Ctrl+s` | Stop instance(s) |
| `Ctrl+x` | Stop all idle instances |
| `Ctrl+d` | Delete instance(s) |
| `Ctrl+r` | Resume stopped instance |
| `Space` | Toggle multi-select |
| `Ctrl+a` | Select all |
| `Ctrl+g` | Group selected instances |
| `Ctrl+b` | Ungroup / break tiled view |
| `/` | Filter instances |
| `L` | Load profile |
| `?` | Help overlay |
| `q` | Quit |

### Tmux navigation

| Key | Action |
|---|---|
| `Ctrl+Space` | Return to dashboard |
| `Ctrl+←` / `Ctrl+→` | Previous / next window |
| `Ctrl+h/j/k/l` | Navigate panes (in tiled view) |

### Mouse

| Action | Effect |
|---|---|
| Left-click | Attach to instance |
| Right-click | Context menu (Attach, Stop, Resume, Toggle Danger, Delete, Ungroup) |

## Configuration

Config file: `~/.config/claudes/config.yaml`

```yaml
poll_interval_ms: 2000    # Status polling interval (default: 2000)
preview_lines: 30         # Output capture lines (default: 30)
default_dir: ~/projects   # Default working directory for new instances
profile_dir: ~/.config/claudes/profiles  # Profile directory
```

All fields are optional with sensible defaults.

## Profiles

Profiles let you launch a predefined set of instances in one action. Store them in `~/.config/claudes/profiles/`.

```yaml
# ~/.config/claudes/profiles/my-project.yaml
name: my-project
instances:
  - name: backend
    dir: ~/projects/api
    task: "Work on the API server"
    dangerous: false

  - name: frontend
    dir: ~/projects/web
    task: "Implement the new dashboard"

  - name: tests
    dir: ~/projects/api
    task: "Fix failing integration tests"
    dangerous: true
```

Press `L` in the dashboard to browse and load profiles.

## Instance Lifecycle

```
  [New]  ──→  Running  ──→  Idle (waiting for input)
                 │              │
                 │         [Ctrl+s / Stop]
                 │              │
                 ▼              ▼
              Stopped  ←──  Stopped
                 │
            [Ctrl+r / Resume]
                 │
                 ▼
              Running (session restored)
```

- **Running** — Claude is actively processing
- **Idle** — Claude finished a turn, waiting for user input
- **Stopped** — Process exited; session ID preserved for resume
- **Error** — Something went wrong; can still resume

## Grouping & Tiled Views

1. Select multiple instances with `Space` or `Ctrl+a`
2. Press `Ctrl+g` to group them
3. Press `Enter` on any group member to open a tiled tmux view
4. Navigate between panes with `Ctrl+h/j/k/l`
5. Press `Ctrl+Space` to return to the dashboard
6. Press `Ctrl+b` to ungroup / break the tiled view

## Data Storage

| Path | Purpose |
|---|---|
| `~/.config/claudes/config.yaml` | Configuration |
| `~/.config/claudes/claudes.db` | Instance persistence (SQLite) |
| `~/.config/claudes/profiles/` | Profile definitions |

The app also reads Claude's session data from `~/.claude/projects/` for activity tracking and session resume.

## Building

```bash
git clone https://github.com/arlintdev/claudestui.git
cd claudestui
go build -o panes .
```

## License

MIT
