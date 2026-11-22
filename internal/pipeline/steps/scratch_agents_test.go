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
