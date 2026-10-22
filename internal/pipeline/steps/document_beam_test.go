package steps

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/agent"
)

const beamReadmeDiff = `diff --git a/README.md b/README.md
new file mode 100644
index 0000000..0000000 100644
--- /dev/null
+++ b/README.md
@@ -0,0 +1 @@
+# Beam
`

func beamDiffResult(diff string) *agent.Result {
	raw, _ := json.Marshal(map[string]any{"diff": diff, "summary": "add README", "findings": []string{}})
	return &agent.Result{Output: json.RawMessage(raw)}
}

func beamDocStandard(runCalls *int, content string) func(context.Context, agent.RunOpts) (*agent.Result, error) {
	return func(_ context.Context, opts agent.RunOpts) (*agent.Result, error) {
		*runCalls++
		if content != "" {
			if err := os.WriteFile(filepath.Join(opts.CWD, "README.md"), []byte(content), 0o644); err != nil {
				return nil, err
			}
		}
		return &agent.Result{Output: json.RawMessage(`{"findings":[],"summary":"verify clean"}`)}, nil
	}
}

func TestBeamDocument_AppliesCheckedDiff(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)
	gitCmd(t, dir, "checkout", "--detach", headSHA)
	var runCalls int
	ag := newMockFreeModelAgent(beamDocStandard(&runCalls, ""))
	ag.freeFn = func(context.Context, agent.FreeModelKind, agent.RunOpts) (*agent.Result, error) {
		return beamDiffResult(beamReadmeDiff), nil
	}
	sctx := freeModelStepContext(t, ag, dir, baseSHA, headSHA)
	outcome, err := (&DocumentStep{}).Execute(sctx)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.NeedsApproval {
		t.Fatal("beam document pass parked")
	}
	if ag.freeCalls != 1 {
		t.Fatalf("free calls = %d, want 1", ag.freeCalls)
	}
	if runCalls != 1 {
		t.Fatalf("verification calls = %d, want the lint-combined verification turn", runCalls)
	}
	content, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil || string(content) != "# Beam"+"\n" {
		t.Fatalf("README.md = %q, want the beam draft", content)
	}
	if status := gitStatusPorcelain(t, dir); status != "" {
		t.Fatalf("worktree not clean after doc commit: %q", status)
	}
}

func TestBeamDocument_EmptyFallsBackToStandard(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)
	gitCmd(t, dir, "checkout", "--detach", headSHA)
	var runCalls int
	ag := newMockFreeModelAgent(beamDocStandard(&runCalls, "# Standard"+"\n"))
	ag.freeFn = func(context.Context, agent.FreeModelKind, agent.RunOpts) (*agent.Result, error) {
		return &agent.Result{}, nil
	}
	ag.fallbackFn = func(context.Context, agent.FreeModelKind, agent.RunOpts) (*agent.Result, error) {
		return &agent.Result{}, nil
	}
	sctx := freeModelStepContext(t, ag, dir, baseSHA, headSHA)
	outcome, err := (&DocumentStep{}).Execute(sctx)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.NeedsApproval {
		t.Fatal("document pass parked")
	}
	if ag.freeCalls != 1 {
		t.Fatalf("free calls = %d, want the single empty turn (the wrapper's own retry is covered in internal/agent)", ag.freeCalls)
	}
	if ag.fallbackCalls != 1 {
		t.Fatalf("fallback calls = %d, want 1", ag.fallbackCalls)
	}
	if runCalls != 1 {
		t.Fatalf("standard calls = %d, want 1", runCalls)
	}
	content, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil || string(content) != "# Standard"+"\n" {
		t.Fatalf("README.md = %q, want the standard draft", content)
	}
}

func TestBeamDocument_UnappliableDiffRestoresAndFallsBack(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)
	gitCmd(t, dir, "checkout", "--detach", headSHA)
	var runCalls int
	ag := newMockFreeModelAgent(beamDocStandard(&runCalls, "# Standard"+"\n"))
	ag.freeFn = func(context.Context, agent.FreeModelKind, agent.RunOpts) (*agent.Result, error) {
		return beamDiffResult("diff --git a/MISSING.md b/MISSING.md" + "\n"), nil
	}
	sctx := freeModelStepContext(t, ag, dir, baseSHA, headSHA)
	if _, err := (&DocumentStep{}).Execute(sctx); err != nil {
		t.Fatal(err)
	}
	if runCalls != 1 {
		t.Fatalf("standard calls = %d, want 1 after the unusable diff", runCalls)
	}
	if _, err := os.Stat(filepath.Join(dir, "MISSING.md")); !os.IsNotExist(err) {
		t.Fatal("unusable beam diff landed on the worktree")
	}
	content, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil || string(content) != "# Standard"+"\n" {
		t.Fatalf("README.md = %q, want the standard draft", content)
	}
}

func TestBeamDocument_DirectEditIsDiscarded(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)
	gitCmd(t, dir, "checkout", "--detach", headSHA)
	var runCalls int
	ag := newMockFreeModelAgent(beamDocStandard(&runCalls, "# Standard"+"\n"))
	ag.freeFn = func(_ context.Context, _ agent.FreeModelKind, opts agent.RunOpts) (*agent.Result, error) {
		if err := os.WriteFile(filepath.Join(opts.CWD, "feature.txt"), []byte("direct edit"), 0o644); err != nil {
			return nil, err
		}
		return beamDiffResult("not a diff"), nil
	}
	sctx := freeModelStepContext(t, ag, dir, baseSHA, headSHA)
	if _, err := (&DocumentStep{}).Execute(sctx); err != nil {
		t.Fatal(err)
	}
	if runCalls != 1 {
		t.Fatalf("standard calls = %d, want 1", runCalls)
	}
	content, err := os.ReadFile(filepath.Join(dir, "feature.txt"))
	if err != nil || string(content) != "feature code"+"\n" {
		t.Fatalf("feature.txt = %q, want the committed content", content)
	}
}

func TestBeamDocument_FailedDraftsRestoreBeforeAnalyzerPass(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)
	gitCmd(t, dir, "checkout", "--detach", headSHA)
	ag := newMockFreeModelAgent(func(context.Context, agent.RunOpts) (*agent.Result, error) {
		return &agent.Result{Output: json.RawMessage(`{"findings":[],"summary":"verify clean"}`)}, nil
	})
	ag.freeFn = func(_ context.Context, _ agent.FreeModelKind, opts agent.RunOpts) (*agent.Result, error) {
		if err := os.WriteFile(filepath.Join(opts.CWD, "feature.txt"), []byte("unchecked beam edit\n"), 0o644); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("beam exploded")
	}
	ag.fallbackFn = func(context.Context, agent.FreeModelKind, agent.RunOpts) (*agent.Result, error) {
		return nil, fmt.Errorf("fallback exploded")
	}
	sctx := freeModelStepContext(t, ag, dir, baseSHA, headSHA)
	if _, err := (&DocumentStep{}).Execute(sctx); err != nil {
		t.Fatal(err)
	}
	if head := gitCmd(t, dir, "rev-parse", "HEAD"); head != headSHA {
		t.Fatalf("HEAD moved to %s: the analyzer pass committed the unchecked beam edit", head)
	}
	content, err := os.ReadFile(filepath.Join(dir, "feature.txt"))
	if err != nil || string(content) != "feature code"+"\n" {
		t.Fatalf("feature.txt = %q, want the committed content", content)
	}
}

func TestBeamDocument_DisabledRunsStandardOnly(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)
	gitCmd(t, dir, "checkout", "--detach", headSHA)
	var runCalls int
	ag := newMockFreeModelAgent(beamDocStandard(&runCalls, "# Standard"+"\n"))
	sctx := freeModelStepContext(t, ag, dir, baseSHA, headSHA)
	sctx.Config.BeamDrafts.Enabled = false
	if _, err := (&DocumentStep{}).Execute(sctx); err != nil {
		t.Fatal(err)
	}
	if ag.freeCalls != 0 {
		t.Fatal("beam ran while disabled")
	}
	if runCalls != 1 {
		t.Fatalf("standard calls = %d, want 1", runCalls)
	}
}

func TestBeamDocument_DocsOnlySkipsVerification(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)
	gitCmd(t, dir, "checkout", "--detach", headSHA)
	var runCalls int
	ag := newMockFreeModelAgent(beamDocStandard(&runCalls, ""))
	ag.freeFn = func(context.Context, agent.FreeModelKind, agent.RunOpts) (*agent.Result, error) {
		return beamDiffResult(beamReadmeDiff), nil
	}
	sctx := freeModelStepContext(t, ag, dir, baseSHA, headSHA)
	sctx.Config.Commands.Lint = "true"
	outcome, err := (&DocumentStep{}).Execute(sctx)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.NeedsApproval {
		t.Fatal("beam document pass parked")
	}
	if runCalls != 0 {
		t.Fatalf("verification calls = %d, want 0 on the docs-only path", runCalls)
	}
	content, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil || string(content) != "# Beam\n" {
		t.Fatalf("README.md = %q, want the beam draft", content)
	}
}

func TestBeamDocument_VerificationFailureFallsBack(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)
	gitCmd(t, dir, "checkout", "--detach", headSHA)
	var runCalls int
	ag := newMockFreeModelAgent(func(_ context.Context, opts agent.RunOpts) (*agent.Result, error) {
		runCalls++
		if runCalls == 1 {
			return &agent.Result{Output: json.RawMessage(`not json`)}, nil
		}
		if err := os.WriteFile(filepath.Join(opts.CWD, "README.md"), []byte("# Standard\n"), 0o644); err != nil {
			return nil, err
		}
		return &agent.Result{Output: json.RawMessage(`{"findings":[],"summary":"standard pass"}`)}, nil
	})
	ag.freeFn = func(context.Context, agent.FreeModelKind, agent.RunOpts) (*agent.Result, error) {
		return beamDiffResult(beamReadmeDiff), nil
	}
	sctx := freeModelStepContext(t, ag, dir, baseSHA, headSHA)
	outcome, err := (&DocumentStep{}).Execute(sctx)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.NeedsApproval {
		t.Fatal("document pass parked")
	}
	if runCalls != 2 {
		t.Fatalf("primary calls = %d, want verification plus the standard pass", runCalls)
	}
	content, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil || string(content) != "# Standard\n" {
		t.Fatalf("README.md = %q, want the standard pass edit", content)
	}
}
