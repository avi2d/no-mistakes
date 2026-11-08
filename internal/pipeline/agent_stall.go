package pipeline

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/procreap"
)

// Bounds for the silence guard that runs once a stall is recorded.
var (
	silenceSpinMinCPU       = 0.8
	silenceSpinSampleWindow = 5 * time.Second
	silenceSpinKillGrace    = procreap.DefaultGrace
)

func stepQuietWarning(cfg *config.Config) time.Duration {
	if cfg == nil || cfg.StepQuietWarning <= 0 {
		return config.DefaultStepQuietWarning
	}
	return cfg.StepQuietWarning
}

// agentStallWatch reports a stall, then guards the silence: a spinning
// descendant leaf is killed, and a turn still silent after a second bound
// ends with that reason.
type agentStallWatch struct {
	quiet        time.Duration
	onStall      func(agentName string, pid int, silentSince time.Time)
	onResume     func()
	onSpinKill   func(procreap.SpinVictim)
	onNoProgress func(reason string)

	mu         sync.Mutex
	agent      string
	heardAt    time.Time
	stalled    bool
	ended      bool
	timer      *time.Timer
	pid        int
	launchedAt time.Time
	exited     bool
	gen        int
	victim     *procreap.SpinVictim
}

func watchAgentStall(agentName string, quiet time.Duration, onStall func(string, int, time.Time), onResume func()) *agentStallWatch {
	w := &agentStallWatch{quiet: quiet, onStall: onStall, onResume: onResume, agent: agentName, heardAt: time.Now()}
	w.mu.Lock()
	w.timer = time.AfterFunc(quiet, w.check)
	w.mu.Unlock()
	return w
}

func (w *agentStallWatch) heard(agentName string) {
	if w == nil {
		return
	}
	w.mu.Lock()
	if w.ended {
		w.mu.Unlock()
		return
	}
	if agentName != "" {
		w.agent = agentName
	}
	w.heardAt = time.Now()
	resumed := w.stalled
	if resumed {
		w.stalled = false
		w.gen++
		w.victim = nil
		w.timer.Reset(w.quiet)
	}
	w.mu.Unlock()
	if resumed {
		w.onResume()
	}
}

// setPID records the native agent subprocess a stall inspection starts from.
func (w *agentStallWatch) setPID(pid int) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.ended {
		return
	}
	w.pid = pid
	w.launchedAt = time.Now()
	w.exited = false
}

// noteExit retires the recorded subprocess: its PID may be reused, so no
// later guard may inspect below it.
func (w *agentStallWatch) noteExit() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.exited = true
}

func (w *agentStallWatch) check() {
	w.mu.Lock()
	if w.ended || w.stalled {
		w.mu.Unlock()
		return
	}
	if silent := time.Since(w.heardAt); silent < w.quiet {
		w.timer.Reset(w.quiet - silent)
		w.mu.Unlock()
		return
	}
	w.stalled = true
	w.gen++
	agent, pid, launchedAt, exited, stallStart, quiet := w.agent, w.pid, w.launchedAt, w.exited, w.heardAt, w.quiet
	gen := w.gen
	w.mu.Unlock()
	w.onStall(agent, pid, stallStart)
	go w.guard(gen, agent, pid, launchedAt, exited, stallStart, quiet)
}

// guard inspects the silent agent's process tree once, then ends a turn that
// still makes no progress after a second bound. An inspection that fails
// only logs: it must never fail the turn it was diagnosing.
func (w *agentStallWatch) guard(gen int, agent string, pid int, launchedAt time.Time, exited bool, stallStart time.Time, quiet time.Duration) {
	if pid > 1 && !exited {
		victim, err := procreap.KillSpinningLeaf(pid, silenceSpinMinCPU, silenceSpinSampleWindow, silenceSpinKillGrace, launchedAt)
		if err != nil {
			slog.Warn("silence guard could not inspect the agent process tree", "agent", agent, "pid", pid, "error", err)
		} else if victim != nil {
			w.mu.Lock()
			if gen == w.gen && w.stalled {
				w.victim = victim
			}
			cb := w.onSpinKill
			w.mu.Unlock()
			if cb != nil {
				cb(*victim)
			}
		}
	}
	deadline := stallStart.Add(2 * quiet)
	for {
		w.mu.Lock()
		settled := gen != w.gen || !w.stalled
		cb := w.onNoProgress
		victim := w.victim
		name := w.agent
		w.mu.Unlock()
		if settled {
			return
		}
		if wait := time.Until(deadline); wait > 0 {
			pause := time.Second
			if wait < pause {
				pause = wait
			}
			time.Sleep(pause)
			continue
		}
		silent := roundActivity(time.Since(stallStart))
		reason := fmt.Sprintf("%s has produced no output for %s with no progress since the stall warning; ending the turn", name, silent)
		if victim != nil {
			reason += fmt.Sprintf(" after killing spinning pid=%d (%s)", victim.PID, truncateCommand(victim.Command, 200))
		}
		if cb != nil {
			cb(reason)
		}
		return
	}
}

func truncateCommand(command string, max int) string {
	runes := []rune(command)
	if len(runes) <= max {
		return command
	}
	return string(runes[:max]) + "..."
}

func (w *agentStallWatch) end() {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.ended = true
	w.gen++
	w.timer.Stop()
	resumed := w.stalled
	if resumed {
		w.stalled = false
	}
	w.mu.Unlock()
	if resumed {
		w.onResume()
	}
}
