package pipeline

import (
	"context"
	"os"
	"strings"
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

func runUpdatedCount(ec *eventCollector) int {
	n := 0
	for _, e := range ec.all() {
		if e.Type == ipc.EventRunUpdated {
			n++
		}
	}
	return n
}

func TestExecutor_SilentAgentIsRecordedAsStalledUntilItSpeaks(t *testing.T) {
	database, p, run, repo := setupTest(t)
	ec := &eventCollector{}
	cfg := &config.Config{StepQuietWarning: 100 * time.Millisecond}

	var stepID string
	var startedAt time.Time
	var stalled *db.StepResult
	var updatesBeforeStall, updatesAtStall, updatesAfterResume int
	silentAgent := &scriptedAgent{script: func(opts agent.RunOpts) {
		startedAt = time.Now()
		opts.OnLifecycle(agent.LifecycleEvent{Agent: "pi", Phase: agent.LifecyclePhaseStart, PID: 4242, Message: "pi started pid=4242"})
		updatesBeforeStall = runUpdatedCount(ec)
		stalled = waitForStall(t, database, stepID, true)
		updatesAtStall = runUpdatedCount(ec)
		opts.OnLifecycle(agent.LifecycleEvent{Agent: "pi", Phase: agent.LifecyclePhaseActivity, Message: "pi producing output"})
		waitForStall(t, database, stepID, false)
		updatesAfterResume = runUpdatedCount(ec)
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

	exec := NewExecutor(database, p, cfg, silentAgent, []Step{step}, ec.handler)
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
	if updatesAtStall <= updatesBeforeStall {
		t.Fatal("no run_updated event announced the stall")
	}
	if updatesAfterResume <= updatesAtStall {
		t.Fatal("no run_updated event announced the agent speaking again")
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
	cfg := &config.Config{StepQuietWarning: 50 * time.Millisecond}

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
