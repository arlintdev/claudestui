package tui

import (
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// sysInfo holds cached system resource usage.
type sysInfo struct {
	mu      sync.RWMutex
	cpuPct  float64
	memUsed uint64 // bytes
	memTotal uint64 // bytes
	lastPoll time.Time
}

var sys sysInfo

// pollSysInfo refreshes CPU and memory stats. Rate-limited to once per 3s.
func pollSysInfo() {
	sys.mu.Lock()
	if time.Since(sys.lastPoll) < 3*time.Second {
		sys.mu.Unlock()
		return
	}
	sys.lastPoll = time.Now()
	sys.mu.Unlock()

	cpu := pollCPU()
	memUsed, memTotal := pollMem()

	sys.mu.Lock()
	sys.cpuPct = cpu
	sys.memUsed = memUsed
	sys.memTotal = memTotal
	sys.mu.Unlock()
}

func getSysInfo() (cpuPct float64, memUsed, memTotal uint64) {
	sys.mu.RLock()
	defer sys.mu.RUnlock()
	return sys.cpuPct, sys.memUsed, sys.memTotal
}

func pollCPU() float64 {
	if runtime.GOOS == "darwin" {
		// top -l 1 -n 0 gives a single sample with CPU stats
		out, err := exec.Command("top", "-l", "1", "-n", "0", "-s", "0").Output()
		if err != nil {
			return 0
		}
		for _, line := range strings.Split(string(out), "\n") {
			if strings.HasPrefix(line, "CPU usage:") {
				// "CPU usage: 5.26% user, 3.94% sys, 90.79% idle"
				parts := strings.Fields(line)
				for i, p := range parts {
					if p == "idle" && i > 0 {
						idle, _ := strconv.ParseFloat(strings.TrimRight(parts[i-1], "%"), 64)
						return 100 - idle
					}
				}
			}
		}
	} else {
		// Linux: use /proc/stat or top
		out, err := exec.Command("top", "-bn1").Output()
		if err != nil {
			return 0
		}
		for _, line := range strings.Split(string(out), "\n") {
			if strings.Contains(line, "Cpu(s)") || strings.HasPrefix(line, "%Cpu") {
				parts := strings.Fields(line)
				for i, p := range parts {
					if p == "id," || p == "id" {
						idle, _ := strconv.ParseFloat(parts[i-1], 64)
						return 100 - idle
					}
				}
			}
		}
	}
	return 0
}

func pollMem() (used, total uint64) {
	if runtime.GOOS == "darwin" {
		// sysctl for total memory
		out, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
		if err == nil {
			total, _ = strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64)
		}
		// vm_stat for free/used
		out, err = exec.Command("vm_stat").Output()
		if err == nil {
			var free, inactive, speculative uint64
			for _, line := range strings.Split(string(out), "\n") {
				if v := parseVmStatLine(line, "Pages free"); v > 0 {
					free = v
				}
				if v := parseVmStatLine(line, "Pages inactive"); v > 0 {
					inactive = v
				}
				if v := parseVmStatLine(line, "Pages speculative"); v > 0 {
					speculative = v
				}
			}
			pageSize := uint64(16384) // Apple Silicon default
			if out, err := exec.Command("sysctl", "-n", "hw.pagesize").Output(); err == nil {
				if ps, err := strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64); err == nil {
					pageSize = ps
				}
			}
			available := (free + inactive + speculative) * pageSize
			if total > available {
				used = total - available
			}
		}
	} else {
		// Linux: /proc/meminfo
		out, err := exec.Command("cat", "/proc/meminfo").Output()
		if err == nil {
			var memTotal, memAvailable uint64
			for _, line := range strings.Split(string(out), "\n") {
				if strings.HasPrefix(line, "MemTotal:") {
					memTotal = parseMemInfoKB(line)
				}
				if strings.HasPrefix(line, "MemAvailable:") {
					memAvailable = parseMemInfoKB(line)
				}
			}
			total = memTotal * 1024
			used = (memTotal - memAvailable) * 1024
		}
	}
	return
}

func parseVmStatLine(line, prefix string) uint64 {
	if !strings.HasPrefix(line, prefix) {
		return 0
	}
	parts := strings.Fields(line)
	if len(parts) < 2 {
		return 0
	}
	val := strings.TrimRight(parts[len(parts)-1], ".")
	n, _ := strconv.ParseUint(val, 10, 64)
	return n
}

func parseMemInfoKB(line string) uint64 {
	parts := strings.Fields(line)
	if len(parts) < 2 {
		return 0
	}
	n, _ := strconv.ParseUint(parts[1], 10, 64)
	return n
}

func formatBytes(b uint64) string {
	const gb = 1024 * 1024 * 1024
	return strconv.FormatFloat(float64(b)/float64(gb), 'f', 1, 64) + "Gi"
}
