package pipeline

import (
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/ipc"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

const failedLintOutcomeFindings = `{"findings":[{"id":"lint-1","severity":"warning","description":"linter exited 1","category":"lint-command"}]}`

func TestExecutor_ApproveAtAStillFailingLintCommandGateNeverReachesPush(t *testing.T) {
	database, p, run, repo := setupTest(t)
	parks := make(chan string, 2)
	const lintID = "lint-1"
	attempts := 0
	lint := &adaptiveCallStep{name: types.StepLint, fn: func(*StepContext) (*StepOutcome, error) {
		attempts++
		if attempts == 3 {
			return &StepOutcome{}, nil
		}
		return &StepOutcome{NeedsApproval: true, ExitCode: 1, Findings: failedLintOutcomeFindings}, nil
	}}
	push := &adaptiveCallStep{name: types.StepPush, fn: func(*StepContext) (*StepOutcome, error) {
		return &StepOutcome{}, nil
	}}
	executor := NewExecutor(database, p, &config.Config{}, nil, []Step{lint, push}, lintParkEvents(parks))
	done, _ := startExecutor(t, executor, run, repo, t.TempDir())

	for range 2 {
		waitReviewBudgetPark(t, parks, done)
		if err := executor.Respond(types.StepLint, types.ActionApprove, nil); err == nil || !strings.Contains(err.Error(), "cannot approve lint") {
			t.Fatalf("Approve should leave the failing lint gate parked, got %v", err)
		}
		rows, err := database.GetStepsByRun(run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 2 || rows[1].StepName != types.StepPush || rows[1].Status != types.StepStatusPending {
			t.Fatalf("Push should stay pending while lint fails, got %+v", rows)
		}
		if err := executor.Respond(types.StepLint, types.ActionFix, []string{lintID}); err != nil {
			t.Fatal(err)
		}
	}
	waitExecutorDone(t, done)
	rows, err := database.GetStepsByRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rows[1].Status != types.StepStatusCompleted {
		t.Fatalf("Push should complete after lint passes, got %s", rows[1].Status)
	}
}

func TestExecutor_SkipAfterRefusedLintFixDoesNotEraseTheFailedExitCode(t *testing.T) {
	database, p, run, repo := setupTest(t)
	parks := make(chan string, 2)
	lint := &adaptiveCallStep{name: types.StepLint, fn: func(sctx *StepContext) (*StepOutcome, error) {
		if sctx.Fixing {
			return nil, &ProtectedPathError{Path: "package.lock", Rule: "*.lock"}
		}
		return &StepOutcome{NeedsApproval: true, ExitCode: 1, Findings: failedLintOutcomeFindings}, nil
	}}
	executor := NewExecutor(database, p, &config.Config{}, nil, []Step{lint}, lintParkEvents(parks))
	done, _ := startExecutor(t, executor, run, repo, t.TempDir())

	waitReviewBudgetPark(t, parks, done)
	if err := executor.Respond(types.StepLint, types.ActionFix, []string{"lint-1"}); err != nil {
		t.Fatal(err)
	}
	if raw := waitReviewBudgetPark(t, parks, done); !HasProtectedPathRefusal(raw) {
		t.Fatalf("expected a protected-path refusal, got %s", raw)
	}
	if err := executor.Respond(types.StepLint, types.ActionSkip, nil); err != nil {
		t.Fatal(err)
	}
	waitExecutorDone(t, done)
	rows, err := database.GetStepsByRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if step := rows[0]; step.Status != types.StepStatusSkipped || step.ExitCode == nil || *step.ExitCode != 1 {
		t.Fatalf("skipping a refused fix must preserve the last lint failure: %+v", step)
	}
}

func lintParkEvents(parks chan<- string) EventFunc {
	return func(event ipc.Event) {
		if event.Type == ipc.EventStepCompleted && event.Status != nil &&
			(*event.Status == string(types.StepStatusAwaitingApproval) || *event.Status == string(types.StepStatusFixReview)) {
			parks <- *event.Findings
		}
	}
}
