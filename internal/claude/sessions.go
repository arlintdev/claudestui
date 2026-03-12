package claude

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// SessionInfo holds metadata and token usage for a Claude session.
type SessionInfo struct {
	SessionID   string
	ProjectPath string
	Summary     string
	FirstPrompt string
	Created     time.Time
	Modified    time.Time
	TokensIn    int64
	TokensOut   int64
}

// SessionStore provides cached, rate-limited access to Claude session data.
type SessionStore struct {
	mu          sync.RWMutex
	sessions    []SessionInfo
	lastScan    time.Time
	scanMinGap  time.Duration
	claudeDir   string
}

// NewSessionStore creates a session store that scans ~/.claude/projects/.
func NewSessionStore() *SessionStore {
	home, _ := os.UserHomeDir()
	return &SessionStore{
		claudeDir:  filepath.Join(home, ".claude", "projects"),
		scanMinGap: 30 * time.Second,
	}
}

// Scan walks the Claude projects directory and collects session info.
// Respects rate limiting — won't re-scan within scanMinGap.
func (s *SessionStore) Scan() {
	s.mu.Lock()
	if time.Since(s.lastScan) < s.scanMinGap {
		s.mu.Unlock()
		return
	}
	s.lastScan = time.Now()
	s.mu.Unlock()

	sessions := s.scanDir()

	s.mu.Lock()
	s.sessions = sessions
	s.mu.Unlock()
}

// ForceScan performs a scan regardless of rate limiting.
func (s *SessionStore) ForceScan() {
	s.mu.Lock()
	s.lastScan = time.Time{}
	s.mu.Unlock()
	s.Scan()
}

// All returns all discovered sessions.
func (s *SessionStore) All() []SessionInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]SessionInfo, len(s.sessions))
	copy(out, s.sessions)
	return out
}

// ForDirectory returns sessions matching a working directory, sorted by Modified desc.
func (s *SessionStore) ForDirectory(dir string) []SessionInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var matches []SessionInfo
	for _, sess := range s.sessions {
		if sess.ProjectPath == dir {
			matches = append(matches, sess)
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].Modified.After(matches[j].Modified)
	})
	return matches
}

// TotalTokens returns aggregate token counts across all sessions.
func (s *SessionStore) TotalTokens() (in, out int64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, sess := range s.sessions {
		in += sess.TokensIn
		out += sess.TokensOut
	}
	return
}

// FormatTokens formats a token count as human-readable: "350", "45.2k", "1.2M".
func FormatTokens(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

func (s *SessionStore) scanDir() []SessionInfo {
	entries, err := os.ReadDir(s.claudeDir)
	if err != nil {
		return nil
	}

	var all []SessionInfo
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		projDir := filepath.Join(s.claudeDir, entry.Name())
		sessions := s.scanProject(projDir, entry.Name())
		all = append(all, sessions...)
	}
	return all
}

// sessionsIndex is the structure of sessions-index.json.
type sessionsIndex map[string]sessionsIndexEntry

type sessionsIndexEntry struct {
	Summary      string `json:"summary"`
	FirstPrompt  string `json:"firstPrompt"`
	Created      string `json:"created"`
	Modified     string `json:"modified"`
	MessageCount int    `json:"messageCount"`
	ProjectPath  string `json:"projectPath"`
}

func (s *SessionStore) scanProject(projDir, encodedName string) []SessionInfo {
	indexPath := filepath.Join(projDir, "sessions-index.json")
	data, err := os.ReadFile(indexPath)
	if err != nil {
		return nil
	}

	var index sessionsIndex
	if err := json.Unmarshal(data, &index); err != nil {
		return nil
	}

	var sessions []SessionInfo
	for sessionID, entry := range index {
		created, _ := time.Parse(time.RFC3339, entry.Created)
		modified, _ := time.Parse(time.RFC3339, entry.Modified)

		info := SessionInfo{
			SessionID:   sessionID,
			ProjectPath: entry.ProjectPath,
			Summary:     entry.Summary,
			FirstPrompt: entry.FirstPrompt,
			Created:     created,
			Modified:    modified,
		}

		// Decode project path from directory name if not in index
		if info.ProjectPath == "" {
			info.ProjectPath = decodeProjectPath(encodedName)
		}

		// Stream JSONL for token counts
		jsonlPath := filepath.Join(projDir, sessionID+".jsonl")
		info.TokensIn, info.TokensOut = scanJSONLTokens(jsonlPath)

		sessions = append(sessions, info)
	}
	return sessions
}

// decodeProjectPath converts the encoded directory name back to a path.
// Claude encodes paths by replacing / with - (roughly).
func decodeProjectPath(encoded string) string {
	// The encoding is: replace leading / and all / with -
	// e.g., "-Users-austin-myproject" → "/Users/austin/myproject"
	if !strings.HasPrefix(encoded, "-") {
		return encoded
	}
	return "/" + strings.ReplaceAll(encoded[1:], "-", "/")
}

// jsonlMessage is a minimal struct to extract token usage from JSONL lines.
type jsonlMessage struct {
	Message *struct {
		Usage *struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

func scanJSONLTokens(path string) (tokensIn, tokensOut int64) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB buffer

	for scanner.Scan() {
		line := scanner.Bytes()
		var msg jsonlMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}
		if msg.Message != nil && msg.Message.Usage != nil {
			tokensIn += msg.Message.Usage.InputTokens
			tokensOut += msg.Message.Usage.OutputTokens
		}
	}
	return
}
