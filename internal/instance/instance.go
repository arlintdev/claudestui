package instance

import (
	"fmt"
	"sync"
	"time"
)

// Status represents the running state of a Claude instance.
type Status int

const (
	StatusStopped Status = iota
	StatusRunning
	StatusIdle
	StatusError
)

func (s Status) String() string {
	switch s {
	case StatusRunning:
		return "running"
	case StatusIdle:
		return "idle"
	case StatusError:
		return "error"
	default:
		return "stopped"
	}
}

// Mode represents whether dangerous mode is active.
type Mode int

const (
	ModeSafe Mode = iota
	ModeDanger
)

func (m Mode) String() string {
	if m == ModeDanger {
		return "DANGER"
	}
	return "safe"
}

// Instance represents a managed Claude Code session.
type Instance struct {
	Name      string
	Dir       string
	Task      string
	Model     string // claude model alias (e.g. "sonnet", "opus")
	GroupName string // persistent group membership
	Status    Status
	Mode      Mode
	WindowID  string // tmux window identifier
	SessionID string // Claude session ID for targeted resume
	PanePID   string // tmux pane PID for resource tracking
	StartedAt time.Time
	CPU       float64 // current CPU% from ps
	MemKB     uint64  // current RSS in KB from ps
	Output    []string // last N lines of captured output
	TokensIn  int64    // from JSONL parsing
	TokensOut int64    // from JSONL parsing
}

// Uptime returns a human-readable duration since the instance started.
func (i *Instance) Uptime() string {
	if i.StartedAt.IsZero() {
		return "-"
	}
	d := time.Since(i.StartedAt)
	switch {
	case d < time.Minute:
		return "<1m"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

// Store is the persistence interface for instances.
type Store interface {
	Save(name, dir, task, mode, model, groupName, windowID, sessionID, startedAt string) error
	All() ([]StoreRow, error)
	Delete(name string) error
}

// StoreRow represents a persisted instance from the database.
type StoreRow struct {
	Name      string
	Dir       string
	Task      string
	Mode      string
	Model     string
	GroupName string
	WindowID  string
	SessionID string
	StartedAt string
}

// Manager tracks all active Claude instances.
type Manager struct {
	mu        sync.RWMutex
	instances []*Instance
	store     Store
}

// NewManager creates an instance manager with optional persistence.
// Pass nil for store to disable persistence.
func NewManager(store Store) *Manager {
	return &Manager{store: store}
}

// Add registers a new instance and persists it to the store.
func (m *Manager) Add(inst *Instance) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.instances = append(m.instances, inst)
	m.persist(inst)
}

// Remove deletes an instance by name.
func (m *Manager) Remove(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, inst := range m.instances {
		if inst.Name == name {
			m.instances = append(m.instances[:i], m.instances[i+1:]...)
			return
		}
	}
}

// All returns a snapshot of all instances.
func (m *Manager) All() []*Instance {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Instance, len(m.instances))
	copy(out, m.instances)
	return out
}

// Get returns an instance by name.
func (m *Manager) Get(name string) *Instance {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, inst := range m.instances {
		if inst.Name == name {
			return inst
		}
	}
	return nil
}

// ByIndex returns an instance by its position in the list.
func (m *Manager) ByIndex(idx int) *Instance {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if idx < 0 || idx >= len(m.instances) {
		return nil
	}
	return m.instances[idx]
}

// Count returns the number of instances.
func (m *Manager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.instances)
}

// Stats returns aggregate counts across all instances.
func (m *Manager) Stats() (total, running, idle, stopped, errored int, tokensIn, tokensOut int64) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	total = len(m.instances)
	for _, inst := range m.instances {
		switch inst.Status {
		case StatusRunning:
			running++
		case StatusIdle:
			idle++
		case StatusStopped:
			stopped++
		case StatusError:
			errored++
		}
		tokensIn += inst.TokensIn
		tokensOut += inst.TokensOut
	}
	return
}

// SaveInstance persists the current state of a named instance to the store.
func (m *Manager) SaveInstance(name string) {
	inst := m.Get(name)
	if inst == nil {
		return
	}
	m.persist(inst)
}

// Delete removes an instance from memory and the store.
func (m *Manager) Delete(name string) {
	m.Remove(name)
	if m.store != nil {
		_ = m.store.Delete(name)
	}
}

// LoadAll loads all instances from the store into memory.
func (m *Manager) LoadAll() {
	if m.store == nil {
		return
	}
	rows, err := m.store.All()
	if err != nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range rows {
		mode := ModeSafe
		if r.Mode == "DANGER" {
			mode = ModeDanger
		}
		var startedAt time.Time
		if r.StartedAt != "" {
			startedAt, _ = time.Parse(time.RFC3339, r.StartedAt)
		}
		inst := &Instance{
			Name:      r.Name,
			Dir:       r.Dir,
			Task:      r.Task,
			Model:     r.Model,
			GroupName: r.GroupName,
			Mode:      mode,
			WindowID:  r.WindowID,
			SessionID: r.SessionID,
			StartedAt: startedAt,
			Status:    StatusStopped, // will be reconciled
		}
		m.instances = append(m.instances, inst)
	}
}

// SetGroup assigns a group name to the given instances and persists.
func (m *Manager) SetGroup(names []string, groupName string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, inst := range m.instances {
		for _, name := range names {
			if inst.Name == name {
				inst.GroupName = groupName
				m.persist(inst)
			}
		}
	}
}

// ClearGroup removes group membership from the given instances and persists.
func (m *Manager) ClearGroup(names []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, inst := range m.instances {
		for _, name := range names {
			if inst.Name == name {
				inst.GroupName = ""
				m.persist(inst)
			}
		}
	}
}

// GroupMembers returns the names of all instances in the given group.
func (m *Manager) GroupMembers(groupName string) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []string
	for _, inst := range m.instances {
		if inst.GroupName == groupName {
			out = append(out, inst.Name)
		}
	}
	return out
}

// persist writes an instance to the store if available.
func (m *Manager) persist(inst *Instance) {
	if m.store == nil {
		return
	}
	startedAt := ""
	if !inst.StartedAt.IsZero() {
		startedAt = inst.StartedAt.Format(time.RFC3339)
	}
	_ = m.store.Save(inst.Name, inst.Dir, inst.Task, inst.Mode.String(),
		inst.Model, inst.GroupName, inst.WindowID, inst.SessionID, startedAt)
}
