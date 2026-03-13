package sshconfig

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ParseHosts reads ~/.ssh/config and returns sorted, deduplicated Host names.
// Wildcards (* and ?) are excluded. Returns an empty slice on any error.
func ParseHosts() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}

	f, err := os.Open(filepath.Join(home, ".ssh", "config"))
	if err != nil {
		return nil
	}
	defer f.Close()

	seen := make(map[string]bool)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(strings.ToLower(line), "host ") {
			continue
		}
		// A single Host line can list multiple names
		for _, name := range strings.Fields(line)[1:] {
			if strings.ContainsAny(name, "*?") {
				continue
			}
			if !seen[name] {
				seen[name] = true
			}
		}
	}

	hosts := make([]string, 0, len(seen))
	for h := range seen {
		hosts = append(hosts, h)
	}
	sort.Strings(hosts)
	return hosts
}
