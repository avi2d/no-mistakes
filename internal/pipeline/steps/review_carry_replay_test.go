package steps

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

// These tests replay recorded review runs through the real executor and the
// real review step: the reviewer's turns keep their recorded ids, files,
// lines, severities, actions and coverage records, and the fixer makes the
// recorded kind of change. Descriptions are shortened, since the recorded
// ones describe a private repository.

func replayRepo(t *testing.T, base, branch map[string]string) (string, string, string) {
	t.Helper()
	dir := t.TempDir()
	write := func(files map[string]string) {
		for name, content := range files {
			path := filepath.Join(dir, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	gitCmd(t, dir, "init")
	gitCmd(t, dir, "config", "user.name", "test")
	gitCmd(t, dir, "config", "user.email", "test@test.com")
	gitCmd(t, dir, "checkout", "-b", "main")
	write(base)
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-m", "base")
	baseSHA := gitCmd(t, dir, "rev-parse", "HEAD")
	gitCmd(t, dir, "checkout", "-b", "feature")
	write(branch)
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-m", "branch")
	return dir, baseSHA, gitCmd(t, dir, "rev-parse", "HEAD")
}

func replayReviewTurn(t *testing.T, findings []types.Finding, reviewedPaths []string) *agent.Result {
	t.Helper()
	out, err := json.Marshal(map[string]any{
		"findings":       findings,
		"summary":        "replayed review turn",
		"risk_level":     "medium",
		"risk_rationale": "replayed",
		"risk_scope":     "source-or-external",
		"reviewed_paths": reviewedPaths,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &agent.Result{Output: out}
}

// replayAgent answers each review turn and each fix turn in order. A fix turn
// runs its edit in the worktree before it answers.
func replayAgent(t *testing.T, reviews []*agent.Result, fixes []func(workDir string)) *sessionMockAgent {
	t.Helper()
	reviewTurn, fixTurn := 0, 0
	mock := &sessionMockAgent{}
	mock.respond = func(opts agent.RunOpts) *agent.Result {
		switch opts.Purpose {
		case "review":
			if reviewTurn >= len(reviews) {
				t.Errorf("unexpected review turn %d", reviewTurn+1)
				return &agent.Result{Output: []byte(`{}`)}
			}
			reviewTurn++
			return reviews[reviewTurn-1]
		case "review-fix":
			if fixTurn >= len(fixes) {
				t.Errorf("unexpected fix turn %d", fixTurn+1)
				return &agent.Result{Output: []byte(`{}`)}
			}
			fixTurn++
			fixes[fixTurn-1](opts.CWD)
			return &agent.Result{Output: []byte(`{"summary":"apply replayed fix"}`)}
		default:
			t.Errorf("unexpected agent purpose %q", opts.Purpose)
			return &agent.Result{Output: []byte(`{}`)}
		}
	}
	return mock
}

func writeReplayFile(t *testing.T, workDir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(workDir, filepath.FromSlash(name)), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func manualReviewDecisions(cfg *config.Config) { cfg.AutoFix.Review = 0 }

// waitForReviewRound waits until the review step has recorded round rounds
// and parked, and reports false if the run finished first.
func waitForReviewRound(t *testing.T, database *db.DB, runID string, rounds int, done <-chan error) bool {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("execute: %v", err)
			}
			return false
		default:
		}
		if step := reviewStepResult(t, database, runID); step != nil && (step.Status == types.StepStatusAwaitingApproval || step.Status == types.StepStatusFixReview) {
			recorded, err := database.GetRoundsByStep(step.ID)
			if err == nil && len(recorded) >= rounds {
				return true
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("review never parked after round %d nor finished", rounds)
	return false
}

func reviewStepResult(t *testing.T, database *db.DB, runID string) *db.StepResult {
	t.Helper()
	steps, err := database.GetStepsByRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range steps {
		if step.StepName == types.StepReview {
			return step
		}
	}
	return nil
}

func respondToReview(t *testing.T, exec *pipeline.Executor, action types.ApprovalAction, ids ...string) {
	t.Helper()
	if err := exec.Respond(types.StepReview, action, ids); err != nil {
		t.Fatalf("respond %s %v: %v", action, ids, err)
	}
}

// Run 01M446ZZY9RRCC7PWBC6GWVW6D: the round-2 fix put the selected finding's
// file back to the base version, so it left the diff and no later round could
// list it as reviewed.
func TestReviewReplay_FixThatRevertsTheFileToBaseClearsTheFinding(t *testing.T) {
	const (
		keybindings = "harness/pi/keybindings.json"
		copySel     = "harness/pi/extensions/copy-selection.ts"
		copyTest    = "tests/unit/pi-copy-selection.test.ts"
	)
	baseKeybindings := "{\n  \"app.message.copy\": [\"ctrl+x\", \"super+c\"]\n}\n"
	base := map[string]string{
		"README.md":            "# skills\n",
		"bun.lock":             "lock v1\n",
		"docs/contracts.md":    "# contracts\n",
		"docs/links.md":        "# links\n",
		keybindings:            baseKeybindings,
		"package.json":         "{\"name\":\"skills\"}\n",
		"src/generate/link.ts": "export const link = 1\n",
	}
	branch := map[string]string{
		"README.md":            "# skills\ncopy selection\n",
		"bun.lock":             "lock v2\n",
		"docs/contracts.md":    "# contracts\ncopy keys\n",
		"docs/links.md":        "# links\ncopy-selection\n",
		copySel:                "export function onKey(data) { copy(data) }\n",
		keybindings:            "{\n  \"app.message.copy\": []\n}\n",
		"package.json":         "{\"name\":\"skills\",\"pi\":\"1.0.2\"}\n",
		"src/generate/link.ts": "export const link = 2\n",
		copyTest:               "test('copies', () => {})\n",
	}
	allPaths := []string{"README.md", "bun.lock", "docs/contracts.md", "docs/links.md", copySel, keybindings, "package.json", "src/generate/link.ts", copyTest}
	withoutKeybindings := []string{"README.md", "bun.lock", "docs/contracts.md", "docs/links.md", copySel, "package.json", "src/generate/link.ts", copyTest}

	reviews := []*agent.Result{
		replayReviewTurn(t, []types.Finding{
			{ID: "copy-key-release", Severity: "warning", Action: types.ActionAutoFix, File: copySel, Line: 25, Description: "The raw-input listener also copies on key release."},
		}, allPaths),
		replayReviewTurn(t, []types.Finding{
			{ID: "tree-copy-disabled", Severity: "warning", Action: types.ActionAskUser, File: keybindings, Line: 2, Description: "Disabling app.message.copy also removes the tree view's copy shortcut."},
		}, allPaths),
		replayReviewTurn(t, []types.Finding{}, withoutKeybindings),
	}
	fixes := []func(string){
		func(workDir string) {
			writeReplayFile(t, workDir, copySel, "export function onKey(data) { if (!isKeyRelease(data)) copy(data) }\n")
			writeReplayFile(t, workDir, copyTest, "test('copies', () => {})\ntest('release copies nothing', () => {})\n")
		},
		func(workDir string) { writeReplayFile(t, workDir, keybindings, baseKeybindings) },
	}

	workDir, baseSHA, headSHA := replayRepo(t, base, branch)
	mock := replayAgent(t, reviews, fixes)
	exec, database, run, repo := reviewSessionHarnessIn(t, mock, []pipeline.Step{&ReviewStep{}}, workDir, baseSHA, headSHA, manualReviewDecisions)
	done := make(chan error, 1)
	go func() { done <- exec.Execute(context.Background(), run, repo, workDir) }()

	if !waitForReviewRound(t, database, run.ID, 1, done) {
		t.Fatal("round 1 did not park on copy-key-release")
	}
	respondToReview(t, exec, types.ActionFix, "copy-key-release")
	if !waitForReviewRound(t, database, run.ID, 2, done) {
		t.Fatal("round 2 did not park on tree-copy-disabled")
	}
	respondToReview(t, exec, types.ActionFix, "tree-copy-disabled")

	if waitForReviewRound(t, database, run.ID, 3, done) {
		step := reviewStepResult(t, database, run.ID)
		t.Fatalf("round 3 parked although the fix reverted %s to the base: %s", keybindings, derefFindings(step))
	}
	step := reviewStepResult(t, database, run.ID)
	if step.Status != types.StepStatusCompleted {
		t.Fatalf("review status = %s, want completed", step.Status)
	}
	if parsed, err := types.ParseFindingsJSON(derefFindings(step)); err != nil || len(parsed.Items) != 0 {
		t.Fatalf("a finding outlived the revert of its file: %s (%v)", derefFindings(step), err)
	}
}

func derefFindings(step *db.StepResult) string {
	if step == nil || step.FindingsJSON == nil {
		return ""
	}
	return *step.FindingsJSON
}
