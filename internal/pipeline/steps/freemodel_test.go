package steps

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
)

type mockFreeModelAgent struct {
	*mockAgent
	freeCalls     int
	freeFn        func(context.Context, agent.FreeModelKind, agent.RunOpts) (*agent.Result, error)
	fallbackCalls int
	fallbackFn    func(context.Context, agent.FreeModelKind, agent.RunOpts) (*agent.Result, error)
}

func (m *mockFreeModelAgent) RunFreeModel(ctx context.Context, kind agent.FreeModelKind, opts agent.RunOpts) (*agent.Result, error) {
	m.freeCalls++
	if m.freeFn != nil {
		return m.freeFn(ctx, kind, opts)
	}
	return &agent.Result{}, nil
}

func (m *mockFreeModelAgent) RunFallbackModel(ctx context.Context, kind agent.FreeModelKind, opts agent.RunOpts) (*agent.Result, error) {
	m.fallbackCalls++
	if m.fallbackFn != nil {
		return m.fallbackFn(ctx, kind, opts)
	}
	return &agent.Result{}, nil
}

func newMockFreeModelAgent(runFn func(context.Context, agent.RunOpts) (*agent.Result, error)) *mockFreeModelAgent {
	return &mockFreeModelAgent{mockAgent: &mockAgent{name: "test", runFn: runFn}}
}

func freeModelStepContext(t *testing.T, ag agent.Agent, workDir, baseSHA, headSHA string) *pipeline.StepContext {
	t.Helper()
	sctx := newTestContextWithDBRecords(t, ag, workDir, baseSHA, headSHA, config.Commands{})
	sctx.Repo.UpstreamURL = "https://github.com/o/alpha"
	sctx.Config.MinimaxExtraReview = config.MinimaxExtraReview{
		Enabled: true, Model: "free-mini", FallbackModel: "fallback",
		Mode: "advisory", Steps: []string{"review"}, Repos: []string{"alpha"},
	}
	sctx.Config.BeamDrafts = config.BeamDrafts{
		Enabled: true, Model: "free-beam", FallbackModel: "fallback",
		Steps: []string{"commit", "document"}, EmptyRetries: 1, ApplyCheck: true, Repos: []string{"alpha"},
	}
	return sctx
}

func TestFreeModelSelection_Minimax(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)
	ag := newMockFreeModelAgent(nil)
	sctx := freeModelStepContext(t, ag, dir, baseSHA, headSHA)
	if !minimaxExtraReviewAllowed(sctx) {
		t.Fatal("enabled minimax with a listed repo is not allowed")
	}
	sctx.Config.MinimaxExtraReview.Enabled = false
	if minimaxExtraReviewAllowed(sctx) {
		t.Fatal("disabled minimax is allowed")
	}
	sctx.Config.MinimaxExtraReview.Enabled = true
	sctx.Repo.UpstreamURL = "https://github.com/o/beta"
	if minimaxExtraReviewAllowed(sctx) {
		t.Fatal("minimax allowed an unlisted repo")
	}
}

func TestFreeModelSelection_Beam(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)
	ag := newMockFreeModelAgent(nil)
	sctx := freeModelStepContext(t, ag, dir, baseSHA, headSHA)
	if !beamDraftsAllowed(sctx) {
		t.Fatal("enabled beam with a listed repo is not allowed")
	}
	sctx.Config.BeamDrafts.Enabled = false
	if beamDraftsAllowed(sctx) {
		t.Fatal("disabled beam is allowed")
	}
}

func TestWorktreeSnapshot_Restore(t *testing.T) {
	t.Parallel()
	dir, baseSHA, _ := setupGitRepo(t)
	_ = baseSHA
	ctx := context.Background()
	before, err := snapshotWorktree(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if before.mutated(ctx, dir) {
		t.Fatal("clean worktree snapshots as mutated")
	}
	if err := os.WriteFile(filepath.Join(dir, "feature.txt"), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fresh.txt"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !before.mutated(ctx, dir) {
		t.Fatal("dirty worktree snapshots as clean")
	}
	if err := before.restore(ctx, dir); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(dir, "feature.txt"))
	if err != nil || string(content) != "feature code"+"\n" {
		t.Fatalf("feature.txt = %q, want the committed content", content)
	}
	if _, err := os.Stat(filepath.Join(dir, "fresh.txt")); !os.IsNotExist(err) {
		t.Fatal("untracked file survived the restore")
	}
}

func TestVerifyBeamDiff(t *testing.T) {
	t.Parallel()
	dir, _, _ := setupGitRepo(t)
	good := "diff --git a/README.md b/README.md" + "\n" +
		"new file mode 100644" + "\n" +
		"index 0000000..0000000 100644" + "\n" +
		"--- /dev/null" + "\n" +
		"+++ b/README.md" + "\n" +
		"@@ -0,0 +1 @@\n" +
		"+# Beam" + "\n"
	if err := verifyBeamDiff(context.Background(), dir, good); err != nil {
		t.Fatalf("valid diff rejected: %v", err)
	}
	if err := verifyBeamDiff(context.Background(), dir, "not a diff"); err == nil {
		t.Fatal("garbage diff accepted")
	}
}
