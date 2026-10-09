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

// carriedTags reads each finding's carried tag from a persisted findings
// payload, keyed by finding id.
func carriedTags(t *testing.T, raw string) map[string]string {
	t.Helper()
	var payload struct {
		Findings []map[string]any `json:"findings"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("parse findings %q: %v", raw, err)
	}
	tags := make(map[string]string, len(payload.Findings))
	for _, finding := range payload.Findings {
		id, _ := finding["id"].(string)
		tag, _ := finding["carried"].(string)
		tags[id] = tag
	}
	return tags
}

func assertCarriedTags(t *testing.T, round int, raw string, want map[string]string) {
	t.Helper()
	got := carriedTags(t, raw)
	if len(got) != len(want) {
		t.Errorf("round %d gate shows %v, want the findings %v", round, got, want)
	}
	for id, tag := range want {
		if got[id] != tag {
			t.Errorf("round %d: finding %s carried = %q, want %q", round, id, got[id], tag)
		}
	}
}

// Run 01M4488AQM5TV9S21FPTCV8G72: each rereview reported new findings in the
// file the earlier findings were fixed in, which keeps those fixed findings
// outstanding, and the gate showed them exactly like the new ones.
func TestReviewReplay_GateTagsWhatTheReviewerDidNotReport(t *testing.T) {
	const (
		style     = "harness/pi/extensions/transcript-style.ts"
		styleTest = "tests/unit/transcript-style.test.ts"
	)
	base := map[string]string{
		"README.md":            "# skills\nPi 1.0.0\n",
		"bun.lock":             "lock v1\n",
		"docs/contracts.md":    "# contracts\n",
		"docs/links.md":        "# links\n",
		"package.json":         "{\"name\":\"skills\"}\n",
		"src/generate/link.ts": "export const link = 1\n",
	}
	branch := map[string]string{
		"README.md":            "# skills\nPi 1.0.0\nreply highlight\n",
		"bun.lock":             "lock v2\n",
		"docs/contracts.md":    "# contracts\nreply highlight\n",
		"docs/links.md":        "# links\ntranscript-style\n",
		style:                  "export const style = 1\n",
		"package.json":         "{\"name\":\"skills\",\"pi\":\"1.0.2\"}\n",
		"src/generate/link.ts": "export const link = 2\n",
		styleTest:              "test('styles', () => {})\n",
	}
	allPaths := []string{"README.md", "bun.lock", "docs/contracts.md", "docs/links.md", style, "package.json", "src/generate/link.ts", styleTest}

	reviews := []*agent.Result{
		replayReviewTurn(t, []types.Finding{
			{ID: "R1", Severity: "error", Action: types.ActionAskUser, File: style, Line: 160, Description: "The selected hues are not guaranteed by these mappings."},
			{ID: "R2", Severity: "warning", Action: types.ActionAutoFix, File: style, Line: 99, Description: "Command-position state advances before complete shell prefixes are consumed."},
			{ID: "R3", Severity: "warning", Action: types.ActionAutoFix, File: style, Line: 85, Description: "The lexer treats escaped shell syntax and heredoc data as executable syntax."},
			{ID: "R4", Severity: "warning", Action: types.ActionAutoFix, File: style, Line: 211, Description: "Removing this extension and invoking /reload does not remove reply styling."},
			{ID: "R5", Severity: "warning", Action: types.ActionAutoFix, File: "package.json", Line: 26, Description: "This change requires Pi 1.0.2, but README.md still declares Pi 1.0.0 sufficient."},
		}, allPaths),
		replayReviewTurn(t, []types.Finding{
			{ID: "R1", Severity: "warning", Action: types.ActionAutoFix, File: style, Line: 179, Description: "The Round 1 fix leaves command-position failures reachable through long options."},
			{ID: "R2", Severity: "warning", Action: types.ActionAutoFix, File: style, Line: 93, Description: "The Round 1 lexer fix leaves a sibling word-boundary error after a variable."},
		}, allPaths),
		replayReviewTurn(t, []types.Finding{
			{ID: "review-3", Severity: "warning", Action: types.ActionAutoFix, File: style, Line: 93, Description: "Round 2's redirect fix leaves numbered input redirects misclassified."},
			{ID: "review-4", Severity: "warning", Action: types.ActionAutoFix, File: style, Line: 126, Description: "Round 2 introduces false word continuation after every backtick."},
		}, allPaths),
	}
	fixes := []func(string){
		func(workDir string) {
			writeReplayFile(t, workDir, style, "export const style = 2\n")
			writeReplayFile(t, workDir, styleTest, "test('styles', () => {})\ntest('prefixes', () => {})\n")
			writeReplayFile(t, workDir, "README.md", "# skills\nPi 1.0.2\nreply highlight\n")
		},
		func(workDir string) { writeReplayFile(t, workDir, style, "export const style = 3\n") },
	}

	workDir, baseSHA, headSHA := replayRepo(t, base, branch)
	mock := replayAgent(t, reviews, fixes)
	exec, database, run, repo := reviewSessionHarnessIn(t, mock, []pipeline.Step{&ReviewStep{}}, workDir, baseSHA, headSHA, manualReviewDecisions)
	done := make(chan error, 1)
	go func() { done <- exec.Execute(context.Background(), run, repo, workDir) }()

	if !waitForReviewRound(t, database, run.ID, 1, done) {
		t.Fatal("round 1 did not park")
	}
	assertCarriedTags(t, 1, derefFindings(reviewStepResult(t, database, run.ID)), map[string]string{
		"R1": "", "R2": "", "R3": "", "R4": "", "R5": "",
	})
	if _, err := exec.RespondWithOverrides(types.StepReview, types.ActionFix, []string{"R2", "R3", "R4", "R5"}, []string{"R1"}, nil, nil, ""); err != nil {
		t.Fatalf("respond fix R2-R5 ignoring R1: %v", err)
	}

	if !waitForReviewRound(t, database, run.ID, 2, done) {
		t.Fatal("round 2 did not park")
	}
	assertCarriedTags(t, 2, derefFindings(reviewStepResult(t, database, run.ID)), map[string]string{
		"R1": "unselected", "R2": "awaiting_verification", "R3": "awaiting_verification", "R4": "awaiting_verification",
		"review-1": "", "review-2": "",
	})
	respondToReview(t, exec, types.ActionFix, "review-1", "review-2")

	if !waitForReviewRound(t, database, run.ID, 3, done) {
		t.Fatal("round 3 did not park")
	}
	step := reviewStepResult(t, database, run.ID)
	assertCarriedTags(t, 3, derefFindings(step), map[string]string{
		"R1": "unselected", "R2": "awaiting_verification", "R3": "awaiting_verification", "R4": "awaiting_verification",
		"review-1": "awaiting_verification", "review-2": "awaiting_verification",
		"review-3": "", "review-4": "",
	})
	rounds, err := database.GetRoundsByStep(step.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := carriedTags(t, *rounds[1].FindingsJSON); got["R2"] != "awaiting_verification" || got["review-1"] != "" {
		t.Errorf("round 2's own record lost what that round's reviewer reported: %v", got)
	}

	respondToReview(t, exec, types.ActionApprove)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("execute: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("executor did not finish after approval")
	}
}
