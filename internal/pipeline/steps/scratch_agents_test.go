package steps

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/scm"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func TestAgentFixStepsDoNotCommitScratch(t *testing.T) {
	for _, step := range []types.StepName{types.StepReview, types.StepTest, types.StepLint} {
		t.Run(string(step), func(t *testing.T) {
			dir, base, head := setupGitRepo(t)
			gitCmd(t, dir, "checkout", "--detach", head)
			ag := &mockAgent{name: "test", runFn: func(_ context.Context, _ agent.RunOpts) (*agent.Result, error) {
				if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("real edit\n"), 0o644); err != nil {
					return nil, err
				}
				if err := os.WriteFile(filepath.Join(dir, "scratch.sh"), []byte("scratch\n"), 0o644); err != nil {
					return nil, err
				}
				return &agent.Result{Output: json.RawMessage(`{"summary":"fix","edited_paths":["README.md"]}`)}, nil
			}}
			sctx := newTestContextWithDBRecords(t, ag, dir, base, head, config.Commands{})
			sctx.Fixing = true
			if _, err := executeFixMode(sctx, step, fixExecutionOptions{Prompt: "fix"}); err != nil {
				t.Fatal(err)
			}
			assertScratchExcludedFromCommit(t, dir)
		})
	}
}

func TestStandaloneLintDoesNotCommitScratch(t *testing.T) {
	dir, base, head := setupGitRepo(t)
	gitCmd(t, dir, "checkout", "--detach", head)
	ag := &mockAgent{name: "test", runFn: func(_ context.Context, _ agent.RunOpts) (*agent.Result, error) {
		if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("real edit\n"), 0o644); err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(dir, "scratch.sh"), []byte("scratch\n"), 0o644); err != nil {
			return nil, err
		}
		return &agent.Result{Output: json.RawMessage(`{"summary":"fix","findings":[],"edited_paths":["README.md"]}`)}, nil
	}}
	sctx := newTestContextWithDBRecords(t, ag, dir, base, head, config.Commands{})
	if _, err := (&LintStep{}).Execute(sctx); err != nil {
		t.Fatal(err)
	}
	assertScratchExcludedFromCommit(t, dir)
}

func TestCIRepairDoesNotCommitScratch(t *testing.T) {
	dir, base, head := setupGitRepo(t)
	gitCmd(t, dir, "checkout", "--detach", head)
	ag := &mockAgent{name: "test", runFn: func(_ context.Context, _ agent.RunOpts) (*agent.Result, error) {
		if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("real edit\n"), 0o644); err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(dir, "scratch.sh"), []byte("scratch\n"), 0o644); err != nil {
			return nil, err
		}
		return &agent.Result{Output: json.RawMessage(`{"summary":"fix","code_change_needed":true,"edited_paths":["README.md"]}`)}, nil
	}}
	sctx := newTestContextWithDBRecords(t, ag, dir, base, head, config.Commands{})
	pr := &scm.PR{Number: "42", URL: "https://github.com/test/repo/pull/42"}
	if _, err := (&CIStep{}).autoFixCI(sctx, &forgejoLogTestHost{}, pr, ciTargetsFor([]string{"test"}, false)); err != nil {
		t.Fatal(err)
	}
	assertScratchExcludedFromCommit(t, dir)
}

func TestMergeResolverDoesNotStageScratchLater(t *testing.T) {
	f := newMergeFixture(t, true)
	ag := &mockAgent{name: "test", runFn: func(_ context.Context, _ agent.RunOpts) (*agent.Result, error) {
		writeFixtureFile(t, f.dir, "shared.txt", "main line\nfeature line\n")
		writeFixtureFile(t, f.dir, "scratch.sh", "scratch\n")
		fixtureGit(t, f.dir, "add", "shared.txt")
		fixtureGit(t, f.dir, "commit", "--no-edit")
		return &agent.Result{Output: json.RawMessage(`{"summary":"resolve","edited_paths":["shared.txt"]}`)}, nil
	}}
	sctx := f.context(t, ag, config.RebaseStrategyMerge)
	if err := mergeWithAgent(context.Background(), sctx, f.mainSHA); err != nil {
		t.Fatal(err)
	}
	if err := stagePipelineChanges(sctx); err != nil {
		t.Fatal(err)
	}
	if files := gitCmd(t, f.dir, "diff", "--cached", "--name-only"); strings.Contains(files, "scratch.sh") {
		t.Fatalf("staged scratch: %s", files)
	}
	if got := gitCmd(t, f.dir, "show", "HEAD:shared.txt"); !strings.Contains(got, "feature line") {
		t.Fatalf("missing resolution: %s", got)
	}
	if _, err := os.Stat(filepath.Join(f.dir, "scratch.sh")); err != nil {
		t.Fatal(err)
	}
}

func TestMergeResolverRejectsCommittedScratchWithoutLosingIt(t *testing.T) {
	f := newMergeFixture(t, true)
	ag := &mockAgent{name: "test", runFn: func(_ context.Context, _ agent.RunOpts) (*agent.Result, error) {
		writeFixtureFile(t, f.dir, "shared.txt", "main line\nfeature line\n")
		writeFixtureFile(t, f.dir, "scratch.sh", "scratch\n")
		fixtureGit(t, f.dir, "add", "shared.txt", "scratch.sh")
		fixtureGit(t, f.dir, "commit", "--no-edit")
		return &agent.Result{Output: json.RawMessage(`{"summary":"resolve","edited_paths":["shared.txt"]}`)}, nil
	}}
	sctx := f.context(t, ag, config.RebaseStrategyMerge)
	if err := mergeWithAgent(context.Background(), sctx, f.mainSHA); err == nil {
		t.Fatal("accepted committed scratch")
	}
	if head := gitCmd(t, f.dir, "rev-parse", "HEAD"); head != f.headSHA {
		t.Fatalf("head = %s, want %s", head, f.headSHA)
	}
	data, err := os.ReadFile(filepath.Join(f.dir, "scratch.sh"))
	if err != nil || string(data) != "scratch\n" {
		t.Fatalf("scratch = %q, %v", data, err)
	}
}

func assertScratchExcludedFromCommit(t *testing.T, dir string) {
	t.Helper()
	files := gitCmd(t, dir, "diff-tree", "--no-commit-id", "--name-only", "-r", "HEAD")
	if strings.Contains(files, "scratch.sh") || !strings.Contains(files, "README.md") {
		t.Fatalf("committed files: %s", files)
	}
	if _, err := os.Stat(filepath.Join(dir, "scratch.sh")); err != nil {
		t.Fatal(err)
	}
}
