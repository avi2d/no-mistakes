package steps

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/git"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

type historyRewriteFixture struct {
	dir, baseSHA, submitted, movedMain, roundStart string
}

func newHistoryRewriteFixture(t *testing.T) historyRewriteFixture {
	t.Helper()
	dir, baseSHA, submitted := setupGitRepo(t)
	gitCmd(t, dir, "checkout", "--detach", baseSHA)
	if err := os.WriteFile(filepath.Join(dir, "base.txt"), []byte("main moved\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "commit", "-am", "main moved under the branch")
	movedMain := gitCmd(t, dir, "rev-parse", "HEAD")
	gitCmd(t, dir, "checkout", "--detach", submitted)
	gitCmd(t, dir, "merge", "--no-ff", "--no-edit", movedMain)
	return historyRewriteFixture{
		dir: dir, baseSHA: baseSHA, submitted: submitted, movedMain: movedMain,
		roundStart: gitCmd(t, dir, "rev-parse", "HEAD"),
	}
}

func gitMayFail(dir string, args ...string) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com",
	)
	_ = cmd.Run()
}

func TestReviewStep_FixRoundThatRewritesHistoryIsDiscarded(t *testing.T) {
	t.Parallel()

	rewrites := []struct {
		name    string
		rewrite func(t *testing.T, f historyRewriteFixture)
	}{
		{
			name: "rebase onto the moved base",
			rewrite: func(t *testing.T, f historyRewriteFixture) {
				gitCmd(t, f.dir, "rebase", f.movedMain)
				if err := os.WriteFile(filepath.Join(f.dir, "CHANGELOG.md"), []byte("regenerated\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "amend the round's starting commit",
			rewrite: func(t *testing.T, f historyRewriteFixture) {
				if err := os.WriteFile(filepath.Join(f.dir, "feature.txt"), []byte("amended\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				gitCmd(t, f.dir, "commit", "--amend", "-am", "amended merge")
			},
		},
		{
			name: "reset back to the submitted head",
			rewrite: func(t *testing.T, f historyRewriteFixture) {
				gitCmd(t, f.dir, "reset", "--hard", f.submitted)
			},
		},
		{
			name: "rebase left stopped on a conflict",
			rewrite: func(t *testing.T, f historyRewriteFixture) {
				gitCmd(t, f.dir, "reset", "--hard", f.submitted)
				if err := os.WriteFile(filepath.Join(f.dir, "base.txt"), []byte("branch edit\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				gitCmd(t, f.dir, "commit", "-am", "conflicting branch edit")
				gitMayFail(f.dir, "rebase", f.movedMain)
				if !rebaseInProgress(context.Background(), f.dir) {
					t.Fatal("fixture rebase did not stop on its conflict")
				}
			},
		},
	}

	for _, tc := range rewrites {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newHistoryRewriteFixture(t)
			ag := &mockAgent{
				name: "test",
				runFn: func(_ context.Context, _ agent.RunOpts) (*agent.Result, error) {
					tc.rewrite(t, f)
					return &agent.Result{Output: json.RawMessage(`{"summary":"regenerate changelog"}`)}, nil
				},
			}
			sctx := newTestContextWithDBRecords(t, ag, f.dir, f.baseSHA, f.roundStart, config.Commands{})
			sctx.Fixing = true
			sctx.PreviousFindings = `{"findings":[{"id":"review-1","severity":"warning","file":"CHANGELOG.md","description":"regenerate CHANGELOG.md for the newly merged PR","action":"auto-fix"}],"summary":"1 issue"}`

			_, err := (&ReviewStep{}).Execute(sctx)
			if err == nil {
				t.Fatal("a fix round that rewrote history was accepted")
			}
			for _, want := range []string{"review fix round rewrote branch history", f.roundStart, "never rebase, reset, or amend", "discarded"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err, want)
				}
			}
			if strings.Contains(err.Error(), "out-of-band") {
				t.Errorf("error blames an out-of-band writer for the fix round's own rewrite: %v", err)
			}
			if got := gitCmd(t, f.dir, "rev-parse", "HEAD"); got != f.roundStart {
				t.Fatalf("worktree HEAD = %s, want the recorded head %s restored", got, f.roundStart)
			}
			if rebaseInProgress(context.Background(), f.dir) {
				t.Fatal("the discarded round left a rebase in progress")
			}
			if status := gitCmd(t, f.dir, "status", "--porcelain", "--untracked-files=no"); status != "" {
				t.Fatalf("tracked files still carry the discarded round: %q", status)
			}
			if sctx.Run.HeadSHA != f.roundStart {
				t.Fatalf("recorded head = %s, want %s", sctx.Run.HeadSHA, f.roundStart)
			}
			stored, err := sctx.DB.GetRun(sctx.Run.ID)
			if err != nil || stored.HeadSHA != f.roundStart {
				t.Fatalf("durable head = %#v (err %v), want %s", stored, err, f.roundStart)
			}
			if len(ag.calls) != 1 {
				t.Fatalf("agent calls = %d, want only the fix turn: the rewritten round must not reach review", len(ag.calls))
			}
		})
	}
}

func TestReviewStep_FixRoundPromptForbidsRewritingHistory(t *testing.T) {
	t.Parallel()
	f := newHistoryRewriteFixture(t)
	calls := 0
	ag := &mockAgent{
		name: "test",
		runFn: func(_ context.Context, _ agent.RunOpts) (*agent.Result, error) {
			calls++
			if calls == 1 {
				if err := os.WriteFile(filepath.Join(f.dir, "CHANGELOG.md"), []byte("regenerated\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				gitCmd(t, f.dir, "add", "CHANGELOG.md")
				gitCmd(t, f.dir, "commit", "-m", "agent commit on top")
				return &agent.Result{Output: json.RawMessage(`{"summary":"regenerate changelog"}`)}, nil
			}
			findings := cleanReviewFindings()
			findings.ReviewedPaths = fullReviewCoverage(t, f.dir, f.baseSHA)
			out, _ := json.Marshal(findings)
			return &agent.Result{Output: out}, nil
		},
	}
	sctx := newTestContextWithDBRecords(t, ag, f.dir, f.baseSHA, f.roundStart, config.Commands{})
	sctx.Fixing = true
	sctx.PreviousFindings = `{"findings":[{"id":"review-1","severity":"warning","file":"CHANGELOG.md","description":"regenerate CHANGELOG.md","action":"auto-fix"}],"summary":"1 issue"}`

	if _, err := (&ReviewStep{}).Execute(sctx); err != nil {
		t.Fatalf("a fix round that only added a commit on top was refused: %v", err)
	}
	if !strings.Contains(ag.calls[0].Prompt, "never rebase, reset, or amend") {
		t.Fatalf("fix prompt does not forbid rewriting history:\n%s", ag.calls[0].Prompt)
	}
	if _, err := git.Run(context.Background(), f.dir, "merge-base", "--is-ancestor", f.roundStart, sctx.Run.HeadSHA); err != nil {
		t.Fatalf("recorded head %s does not descend from the round's start %s", sctx.Run.HeadSHA, f.roundStart)
	}
}

func TestExecuteFixMode_EveryFixRoundRefusesHistoryRewrite(t *testing.T) {
	t.Parallel()
	for _, stepName := range []types.StepName{types.StepTest, types.StepLint} {
		t.Run(string(stepName), func(t *testing.T) {
			t.Parallel()
			f := newHistoryRewriteFixture(t)
			ag := &mockAgent{
				name: "test",
				runFn: func(_ context.Context, _ agent.RunOpts) (*agent.Result, error) {
					gitCmd(t, f.dir, "rebase", f.movedMain)
					return &agent.Result{Output: json.RawMessage(`{"summary":"fix"}`)}, nil
				},
			}
			sctx := newTestContextWithDBRecords(t, ag, f.dir, f.baseSHA, f.roundStart, config.Commands{})
			sctx.Fixing = true

			_, err := executeFixMode(sctx, stepName, fixExecutionOptions{Prompt: "fix it", FallbackSummary: "fix"})
			if err == nil || !strings.Contains(err.Error(), string(stepName)+" fix round rewrote branch history") {
				t.Fatalf("%s fix round rewrite error = %v", stepName, err)
			}
			if !strings.Contains(ag.calls[0].Prompt, "never rebase, reset, or amend") {
				t.Fatalf("%s fix prompt does not forbid rewriting history", stepName)
			}
			if got := gitCmd(t, f.dir, "rev-parse", "HEAD"); got != f.roundStart {
				t.Fatalf("%s worktree HEAD = %s, want %s", stepName, got, f.roundStart)
			}
		})
	}
}
