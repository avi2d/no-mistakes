package pipeline

import (
	"context"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/ipc"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

// scriptedAgent runs script in place of a native adapter's subprocess.
type scriptedAgent struct {
	script func(opts agent.RunOpts)
}

func (a *scriptedAgent) Name() string { return "pi" }

func (a *scriptedAgent) Close() error { return nil }

func (a *scriptedAgent) Run(_ context.Context, opts agent.RunOpts) (*agent.Result, error) {
	a.script(opts)
	return &agent.Result{Text: "done"}, nil
}

// A loaded runner delays a scripted step past 200ms. The silence guard ends a
// turn one quiet period after the stall, and a silent agent stalls again one
// quiet period after it speaks, so each step must land inside that period.
const stallTestQuiet = time.Second

func waitForStall(t *testing.T, database *db.DB, stepID string, want bool) *db.StepResult {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, err := database.GetStepResult(stepID)
		if err != nil {
			t.Fatalf("get step result: %v", err)
		}
		if (got.Stall != nil) == want {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("stall = %+v, want stalled=%v", got.Stall, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type stallAnnouncements struct {
	database *db.DB
	watched  atomic.Pointer[string]
	seen     chan bool
}

func newStallAnnouncements(database *db.DB) *stallAnnouncements {
	return &stallAnnouncements{database: database, seen: make(chan bool, 64)}
}

// The handler reads on the emitter's goroutine through a one-connection pool,
// so a watch spans only the agent turn, where only the stall watch emits.
func (s *stallAnnouncements) watch(stepID string) { s.watched.Store(&stepID) }

func (s *stallAnnouncements) unwatch() { s.watched.Store(nil) }

func (s *stallAnnouncements) handler(e ipc.Event) {
	if e.Type != ipc.EventRunUpdated {
		return
	}
	stepID := s.watched.Load()
	if stepID == nil {
		return
	}
	got, err := s.database.GetStepResult(*stepID)
	if err != nil || got == nil {
		return
	}
	select {
	case s.seen <- got.Stall != nil:
	default:
	}
}

func (s *stallAnnouncements) await(t *testing.T, stalled bool) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case got := <-s.seen:
			if got == stalled {
				return
			}
		case <-deadline:
			t.Fatalf("no run_updated event announced stalled=%v", stalled)
		}
	}
}

func TestExecutor_SilentAgentIsRecordedAsStalledUntilItSpeaks(t *testing.T) {
	database, p, run, repo := setupTest(t)
	announcements := newStallAnnouncements(database)
	cfg := &config.Config{StepQuietWarning: stallTestQuiet}

	var stepID string
	var startedAt time.Time
	var stalled *db.StepResult
	silentAgent := &scriptedAgent{script: func(opts agent.RunOpts) {
		announcements.watch(stepID)
		defer announcements.unwatch()
		startedAt = time.Now()
		opts.OnLifecycle(agent.LifecycleEvent{Agent: "pi", Phase: agent.LifecyclePhaseStart, PID: 4242, Message: "pi started pid=4242"})
		announcements.await(t, true)
		stalled = mustStep(t, database, stepID)
		opts.OnLifecycle(agent.LifecycleEvent{Agent: "pi", Phase: agent.LifecyclePhaseActivity, Message: "pi producing output"})
		announcements.await(t, false)
	}}
	step := &adaptiveCallStep{
		name: types.StepReview,
		fn: func(sctx *StepContext) (*StepOutcome, error) {
			stepID = sctx.StepResultID
			if _, err := sctx.RunAgent(agent.RunOpts{Prompt: "review"}); err != nil {
				t.Fatalf("run agent: %v", err)
			}
			return &StepOutcome{ExitCode: 0}, nil
		},
	}

	exec := NewExecutor(database, p, cfg, silentAgent, []Step{step}, announcements.handler)
	if err := exec.Execute(context.Background(), run, repo, t.TempDir()); err != nil {
		t.Fatalf("execute: %v", err)
	}

	if stalled.Stall.Agent != "pi" {
		t.Fatalf("stalled agent = %q, want pi", stalled.Stall.Agent)
	}
	if since := time.Unix(stalled.Stall.SilentSince, 0); since.Before(startedAt.Add(-time.Second)) || since.After(startedAt.Add(time.Second)) {
		t.Fatalf("silent since %s, want the agent's start at %s", since, startedAt)
	}
	if last := derefString(stalled.LastActivity); !strings.HasSuffix(last, "pi started pid=4242") {
		t.Fatalf("last_activity = %q, want the stall notice not to count as agent activity", last)
	}
	logBytes, err := os.ReadFile(*mustStep(t, database, stepID).LogPath)
	if err != nil {
		t.Fatalf("read step log: %v", err)
	}
	if !strings.Contains(string(logBytes), "pi has produced no output for ") {
		t.Fatalf("step log = %q, want a stall notice naming the agent", logBytes)
	}
}

func TestExecutor_StallClearsWhenTheTurnEndsSilent(t *testing.T) {
	database, p, run, repo := setupTest(t)
	cfg := &config.Config{StepQuietWarning: stallTestQuiet}

	var stepID string
	silentAgent := &scriptedAgent{script: func(opts agent.RunOpts) {
		opts.OnLifecycle(agent.LifecycleEvent{Agent: "pi", Phase: agent.LifecyclePhaseStart, PID: 4242, Message: "pi started pid=4242"})
		waitForStall(t, database, stepID, true)
	}}
	step := &adaptiveCallStep{
		name: types.StepReview,
		fn: func(sctx *StepContext) (*StepOutcome, error) {
			stepID = sctx.StepResultID
			if _, err := sctx.RunAgent(agent.RunOpts{Prompt: "review"}); err != nil {
				t.Fatalf("run agent: %v", err)
			}
			if got := mustStep(t, database, stepID); got.Stall != nil {
				t.Fatalf("stall = %+v after the agent returned, want nil", got.Stall)
			}
			return &StepOutcome{ExitCode: 0}, nil
		},
	}

	exec := NewExecutor(database, p, cfg, silentAgent, []Step{step}, nil)
	if err := exec.Execute(context.Background(), run, repo, t.TempDir()); err != nil {
		t.Fatalf("execute: %v", err)
	}
}

func TestExecutor_AgentThatKeepsTalkingIsNeverStalled(t *testing.T) {
	database, p, run, repo := setupTest(t)
	cfg := &config.Config{StepQuietWarning: 400 * time.Millisecond}

	var stepID string
	talkingAgent := &scriptedAgent{script: func(opts agent.RunOpts) {
		opts.OnLifecycle(agent.LifecycleEvent{Agent: "pi", Phase: agent.LifecyclePhaseStart, PID: 4242, Message: "pi started pid=4242"})
		for i := 0; i < 20; i++ {
			time.Sleep(50 * time.Millisecond)
			if i%2 == 0 {
				opts.OnChunk("thinking\n")
			} else {
				opts.OnLifecycle(agent.LifecycleEvent{Agent: "pi", Phase: agent.LifecyclePhaseActivity, Message: "pi producing output"})
			}
			if got := mustStep(t, database, stepID); got.Stall != nil {
				t.Errorf("stall = %+v while the agent kept producing output", got.Stall)
				return
			}
		}
	}}
	step := &adaptiveCallStep{
		name: types.StepReview,
		fn: func(sctx *StepContext) (*StepOutcome, error) {
			stepID = sctx.StepResultID
			if _, err := sctx.RunAgent(agent.RunOpts{Prompt: "review"}); err != nil {
				t.Fatalf("run agent: %v", err)
			}
			return &StepOutcome{ExitCode: 0}, nil
		},
	}

	exec := NewExecutor(database, p, cfg, talkingAgent, []Step{step}, nil)
	if err := exec.Execute(context.Background(), run, repo, t.TempDir()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	logBytes, err := os.ReadFile(*mustStep(t, database, stepID).LogPath)
	if err != nil {
		t.Fatalf("read step log: %v", err)
	}
	if strings.Contains(string(logBytes), "has produced no output") {
		t.Fatalf("step log = %q, want no stall notice for a talking agent", logBytes)
	}
}

func mustStep(t *testing.T, database *db.DB, stepID string) *db.StepResult {
	t.Helper()
	got, err := database.GetStepResult(stepID)
	if err != nil || got == nil {
		t.Fatalf("get step result %s: %v", stepID, err)
	}
	return got
}
