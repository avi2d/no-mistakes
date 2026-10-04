package pipeline

import (
	"sync"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/config"
)

func stepQuietWarning(cfg *config.Config) time.Duration {
	if cfg == nil || cfg.StepQuietWarning <= 0 {
		return config.DefaultStepQuietWarning
	}
	return cfg.StepQuietWarning
}

// agentStallWatch only reports; cancelling a silent turn is the agent budget's job.
type agentStallWatch struct {
	quiet    time.Duration
	onStall  func(agentName string, silentSince time.Time)
	onResume func()

	mu      sync.Mutex
	agent   string
	heardAt time.Time
	stalled bool
	ended   bool
	timer   *time.Timer
}

func watchAgentStall(agentName string, quiet time.Duration, onStall func(string, time.Time), onResume func()) *agentStallWatch {
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
	defer w.mu.Unlock()
	if w.ended {
		return
	}
	if agentName != "" {
		w.agent = agentName
	}
	w.heardAt = time.Now()
	if w.stalled {
		w.stalled = false
		w.timer.Reset(w.quiet)
		w.onResume()
	}
}

func (w *agentStallWatch) check() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.ended || w.stalled {
		return
	}
	if silent := time.Since(w.heardAt); silent < w.quiet {
		w.timer.Reset(w.quiet - silent)
		return
	}
	w.stalled = true
	w.onStall(w.agent, w.heardAt)
}

func (w *agentStallWatch) end() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ended = true
	w.timer.Stop()
	if w.stalled {
		w.stalled = false
		w.onResume()
	}
}
