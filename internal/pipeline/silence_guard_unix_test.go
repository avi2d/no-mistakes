//go:build unix

package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/procreap"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

// bogusAgentPID can never be a live process: it exceeds the kernel PID
// ceiling on every platform this package builds for.
const bogusAgentPID = 1<<22 + 7

func TestExecutor_SilentTurnEndsAfterSecondBound(t *testing.T) {
	database, p, run, repo := setupTest(t)
	cfg := &config.Config{StepQuietWarning: 100 * time.Millisecond}

	silentAgent := &hangingAgent{name: "pi", runFn: func(ctx context.Context, opts agent.RunOpts) (*agent.Result, error) {
		opts.OnLifecycle(agent.LifecycleEvent{Agent: "pi", Phase: agent.LifecyclePhaseStart, PID: bogusAgentPID, Message: "pi started"})
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	step := &adaptiveCallStep{
		name: types.StepReview,
		fn: func(sctx *StepContext) (*StepOutcome, error) {
			if _, err := sctx.RunAgent(agent.RunOpts{Prompt: "work"}); err != nil {
				return nil, err
			}
			return &StepOutcome{ExitCode: 0}, nil
		},
	}

	ex := NewExecutor(database, p, cfg, silentAgent, []Step{step}, nil)
	err := ex.Execute(context.Background(), run, repo, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "ending the turn") {
		t.Fatalf("execute = %v, want the no-progress guard to end the silent turn", err)
	}
	if !strings.Contains(err.Error(), "produced no output") {
		t.Fatalf("execute = %v, want the ending reason to name the silence", err)
	}

	logBytes, readErr := os.ReadFile(filepath.Join(ex.paths.RunLogDir(run.ID), "review.log"))
	if readErr != nil {
		t.Fatalf("read step log: %v", readErr)
	}
	if !strings.Contains(string(logBytes), "has produced no output for ") {
		t.Fatalf("step log = %q, want the stall notice", logBytes)
	}
	if !strings.Contains(string(logBytes), "ending the turn") {
		t.Fatalf("step log = %q, want the no-progress reason", logBytes)
	}
}

func TestExecutor_StallKillsSpinningLeafAndEndsSilentTurn(t *testing.T) {
	oldMinCPU, oldWindow := silenceSpinMinCPU, silenceSpinSampleWindow
	silenceSpinMinCPU, silenceSpinSampleWindow = 0.35, 3*time.Second
	defer func() { silenceSpinMinCPU, silenceSpinSampleWindow = oldMinCPU, oldWindow }()

	cfg := &config.Config{StepQuietWarning: 200 * time.Millisecond}
	// A loaded runner can starve the spinner for a whole sample window, so
	// a first miss gets a second attempt before the test fails.
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		if err = stallKillAttempt(t, cfg); err == nil {
			return
		}
		t.Logf("attempt %d missed: %v", attempt, err)
	}
	t.Fatalf("the guard never killed the spinning leaf: %v", err)
}

func stallKillAttempt(t *testing.T, cfg *config.Config) error {
	t.Helper()
	database, p, run, repo := setupTest(t)

	cmdCh := make(chan *exec.Cmd, 1)
	spinningAgent := &hangingAgent{name: "pi", runFn: func(ctx context.Context, opts agent.RunOpts) (*agent.Result, error) {
		cmd := exec.Command("sh", "-c", "while :; do :; done & wait")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		cmdCh <- cmd
		opts.OnLifecycle(agent.LifecycleEvent{Agent: "pi", Phase: agent.LifecyclePhaseStart, PID: cmd.Process.Pid, Message: "pi started"})
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	step := &adaptiveCallStep{
		name: types.StepReview,
		fn: func(sctx *StepContext) (*StepOutcome, error) {
			if _, err := sctx.RunAgent(agent.RunOpts{Prompt: "work"}); err != nil {
				return nil, err
			}
			return &StepOutcome{ExitCode: 0}, nil
		},
	}

	ex := NewExecutor(database, p, cfg, spinningAgent, []Step{step}, nil)
	if err := ex.Execute(context.Background(), run, repo, t.TempDir()); err == nil || !strings.Contains(err.Error(), "ending the turn") {
		return fmt.Errorf("execute = %v, want the guard to end the silent turn", err)
	}

	var cmd *exec.Cmd
	select {
	case cmd = <-cmdCh:
	default:
		return errors.New("the agent never launched its probe")
	}
	root := cmd.Process.Pid
	defer func() {
		if pgid, pgErr := syscall.Getpgid(root); pgErr == nil {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		}
		_ = cmd.Wait()
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		descendants, descErr := procreap.LiveDescendants(root)
		if descErr != nil {
			return fmt.Errorf("list descendants: %v", descErr)
		}
		if len(descendants) == 0 {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%d descendant(s) still alive after the guard's kill", len(descendants))
		}
		time.Sleep(50 * time.Millisecond)
	}

	logBytes, readErr := os.ReadFile(filepath.Join(ex.paths.RunLogDir(run.ID), "review.log"))
	if readErr != nil {
		return fmt.Errorf("read step log: %v", readErr)
	}
	if !strings.Contains(string(logBytes), "killed spinning pid=") {
		return fmt.Errorf("step log = %q, want the kill logged with its command line", logBytes)
	}
	if !strings.Contains(string(logBytes), "while") {
		return fmt.Errorf("step log = %q, want the victim's command line", logBytes)
	}
	return nil
}
