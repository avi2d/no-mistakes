package procreap

import (
	"sort"
	"strings"
	"time"
)

// SpinVictim is a descendant process that burned CPU while its agent was
// silent, terminated so a wedged probe cannot hold the turn open.
type SpinVictim struct {
	PID         int
	Command     string
	CPUFraction float64
	Killed      bool
}

// cpuState is one process-table entry as the spin probe needs it.
type cpuState struct {
	PID     int
	PPID    int
	PGID    int
	Stat    string
	Elapsed time.Duration
	Total   time.Duration
	Command string
}

// listCPUStatesFunc reads the process table; overridden in tests.
var listCPUStatesFunc = listProcessCPUStates

// KillSpinningLeaf terminates the hottest descendant leaf of root when it
// burns at least minCPU of one core across window, and reports it with its
// command line. Only leaves are candidates: the root is the agent turn
// itself, and a mid-tree parent can own both a spinner and a tool that only
// just started. An unknown, exited, or age-mismatched root ends the probe
// with nothing killed: a PID observed at stall time can name a different
// process by kill time when the agent exited in between and the kernel
// reused it. Errors reading the table are returned; a failed inspection
// must never fail the turn it was diagnosing.
func KillSpinningLeaf(root int, minCPU float64, window, grace time.Duration, launchedAt time.Time) (*SpinVictim, error) {
	if root <= 1 || minCPU <= 0 || window <= 0 {
		return nil, nil
	}
	before, err := listCPUStatesFunc()
	if err != nil {
		return nil, err
	}
	start := time.Now()
	past := indexCPUStates(before)
	rootState, ok := past[root]
	if !ok {
		return nil, nil
	}
	if !launchedAt.IsZero() && !rootAgeMatches(rootState.Elapsed, start.Sub(launchedAt)) {
		return nil, nil
	}
	time.Sleep(window)
	after, err := listCPUStatesFunc()
	if err != nil {
		return nil, err
	}
	pid, frac, cmd, ok := selectSpinningLeaf(root, past, indexCPUStates(after), time.Since(start), minCPU)
	if !ok {
		return nil, nil
	}
	if !processAliveFunc(pid) {
		return nil, nil
	}
	if err := signalProcessFunc(pid, sigTerm); err != nil {
		return nil, err
	}
	victim := &SpinVictim{PID: pid, Command: cmd, CPUFraction: frac}
	if pollProcessExit(pid, grace) {
		return victim, nil
	}
	if err := signalProcessFunc(pid, sigKill); err != nil {
		return nil, err
	}
	victim.Killed = true
	return victim, nil
}

func indexCPUStates(states []cpuState) map[int]cpuState {
	byPID := make(map[int]cpuState, len(states))
	for _, s := range states {
		byPID[s.PID] = s
	}
	return byPID
}

// rootAgeMatches refuses a root whose table age cannot be the launch the
// caller saw: a younger table entry is a reused PID.
func rootAgeMatches(elapsed, expected time.Duration) bool {
	const tolerance = 2 * time.Minute
	diff := elapsed - expected
	return diff <= tolerance && diff >= -tolerance
}

// selectSpinningLeaf names the hottest descendant leaf present in both
// samples. A leaf that vanished, went zombie, or ran its CPU clock backwards
// across the window is never a candidate.
func selectSpinningLeaf(root int, before, after map[int]cpuState, wall time.Duration, minCPU float64) (int, float64, string, bool) {
	if wall <= 0 {
		return 0, 0, "", false
	}
	var states []processState
	for _, s := range before {
		states = append(states, processState{PID: s.PID, PPID: s.PPID, PGID: s.PGID, Stat: s.Stat})
	}
	descendants := liveDescendants(root, states)
	parented := make(map[int]bool, len(descendants))
	for pid := range descendants {
		if _, ok := descendants[before[pid].PPID]; ok {
			parented[before[pid].PPID] = true
		}
	}
	var leaves []int
	for pid := range descendants {
		if !parented[pid] {
			leaves = append(leaves, pid)
		}
	}
	sort.Ints(leaves)
	best := 0
	bestFrac := 0.0
	bestCmd := ""
	for _, pid := range leaves {
		old, ok := before[pid]
		if !ok {
			continue
		}
		fresh, ok := after[pid]
		if !ok || strings.HasPrefix(fresh.Stat, "Z") {
			continue
		}
		if fresh.Total < old.Total {
			continue
		}
		frac := float64(fresh.Total-old.Total) / float64(wall)
		if frac >= minCPU && frac > bestFrac {
			best, bestFrac, bestCmd = pid, frac, fresh.Command
		}
	}
	if best == 0 {
		return 0, 0, "", false
	}
	return best, bestFrac, bestCmd, true
}

// pollProcessExit reports whether pid is gone within grace.
func pollProcessExit(pid int, grace time.Duration) bool {
	if grace <= 0 {
		return !processAliveFunc(pid)
	}
	deadline := time.Now().Add(grace)
	for {
		if !processAliveFunc(pid) {
			return true
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return !processAliveFunc(pid)
		}
		pause := 50 * time.Millisecond
		if remaining < pause {
			pause = remaining
		}
		time.Sleep(pause)
	}
}
