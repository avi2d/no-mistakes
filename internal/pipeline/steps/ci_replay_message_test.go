package steps

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/config"
)

func TestCIStep_RebaseRepairThatRewordsTheWorkersCommitIsNotPublishedReworded(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		message string
		rebase  bool
	}{
		{"rebase rewords subject", "reworded subject", true},
		{"rebase adds footer", "feature part two\n\nCloses: #12", true},
		{"amend rewords subject", "reworded subject", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newReplayFixture(t, true, false)
			want := gitCmd(t, f.dir, "log", "-1", "--format=%B", f.reviewedHead)
			step := &CIStep{}
			if tc.rebase {
				gitCmd(t, f.dir, "rebase", f.base)
				step.replayBase = f.base
			}
			gitCmd(t, f.dir, "commit", "--amend", "-m", tc.message)

			_, err := step.commitRepair(f.sctx, "rebase onto the base", nil)
			if err == nil || !strings.Contains(err.Error(), "commit message") {
				t.Fatalf("a repair that rewrote the worker's commit message was accepted: err=%v\nlog:\n%s", err, f.log())
			}
			if got := f.approval(t); got != f.reviewedHead {
				t.Fatalf("review approval moved to %s, want it left on %s", got, f.reviewedHead)
			}
			if f.sctx.Run.HeadSHA != f.reviewedHead {
				t.Fatalf("recorded head = %s, want %s", f.sctx.Run.HeadSHA, f.reviewedHead)
			}
			if got := gitCmd(t, f.dir, "log", "-1", "--format=%B", "HEAD"); got != want {
				t.Fatalf("worktree head message = %q, want the worker's %q", got, want)
			}
		})
	}
}

func TestRebaseStep_ConflictResolverThatRewordsTheWorkersCommitIsNotKept(t *testing.T) {
	t.Parallel()
	upstream := t.TempDir()
	gitCmd(t, upstream, "init", "--bare")
	dir := t.TempDir()
	gitCmd(t, dir, "init")
	gitCmd(t, dir, "config", "user.name", "test")
	gitCmd(t, dir, "config", "user.email", "test@test.com")
	gitCmd(t, dir, "checkout", "-b", "main")
	gitCmd(t, dir, "remote", "add", "origin", upstream)
	writeFile(t, dir, "shared.txt", "base content\n")
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-m", "base commit")
	baseSHA := gitCmd(t, dir, "rev-parse", "HEAD")
	gitCmd(t, dir, "push", "origin", "main")

	gitCmd(t, dir, "checkout", "-b", "feature")
	writeFile(t, dir, "shared.txt", "feature change\n")
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-m", "feat: feature change\n\nRefs: #7")
	headSHA := gitCmd(t, dir, "rev-parse", "HEAD")
	want := gitCmd(t, dir, "log", "-1", "--format=%B", headSHA)

	gitCmd(t, dir, "checkout", "main")
	writeFile(t, dir, "shared.txt", "main change\n")
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-m", "main conflict")
	gitCmd(t, dir, "push", "origin", "main")
	gitCmd(t, dir, "checkout", "feature")

	ag := &mockAgent{name: "test", runFn: func(context.Context, agent.RunOpts) (*agent.Result, error) {
		writeFile(t, dir, "shared.txt", "resolved content\n")
		gitCmd(t, dir, "add", "shared.txt")
		gitCmd(t, dir, "-c", "core.editor=true", "rebase", "--continue")
		gitCmd(t, dir, "commit", "--amend", "-m", "fix: reworded by the resolver")
		return &agent.Result{Output: json.RawMessage(`{"summary":"resolve shared.txt"}`)}, nil
	}}
	sctx := newTestContextWithDBRecords(t, ag, dir, baseSHA, headSHA, config.Commands{})
	sctx.Run.Branch = "refs/heads/feature"
	sctx.Repo.UpstreamURL = upstream
	sctx.Fixing = true

	_, err := (&RebaseStep{}).Execute(sctx)
	if err == nil || !strings.Contains(err.Error(), "commit message") {
		t.Fatalf("a resolver that reworded the worker's commit was accepted: %v", err)
	}
	if got := gitCmd(t, dir, "rev-parse", "HEAD"); got != headSHA {
		t.Fatalf("HEAD = %s, want the recorded head %s restored", got, headSHA)
	}
	if got := gitCmd(t, dir, "log", "-1", "--format=%B", "HEAD"); got != want {
		t.Fatalf("head message = %q, want the worker's %q", got, want)
	}
}
