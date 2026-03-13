package claude

import (
	"os/exec"
	"strconv"
	"strings"

	"github.com/arlintdev/claudes/internal/instance"
)

// refreshProcStats updates CPU and MemKB for all instances that have a DaemonPID.
func refreshProcStats(instances []*instance.Instance) {
	pidToInst := make(map[string]*instance.Instance)
	var pids []string
	for _, inst := range instances {
		if inst.DaemonPID != 0 && inst.Status != instance.StatusStopped {
			pidStr := strconv.Itoa(inst.DaemonPID)
			pidToInst[pidStr] = inst
			pids = append(pids, pidStr)
		} else {
			inst.CPU = 0
			inst.MemKB = 0
		}
	}
	if len(pids) == 0 {
		return
	}

	args := append([]string{"-o", "pid=,pcpu=,rss=", "-p"}, strings.Join(pids, ","))
	out, err := exec.Command("ps", args...).Output()
	if err != nil {
		return
	}

	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pid := fields[0]
		inst, ok := pidToInst[pid]
		if !ok {
			continue
		}
		inst.CPU, _ = strconv.ParseFloat(fields[1], 64)
		rss, _ := strconv.ParseUint(fields[2], 10, 64)
		inst.MemKB = rss
	}
}
