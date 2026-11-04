//go:build e2e

package e2e

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/types"
)

func TestFixCommitIgnoresAnIdentityAnEarlierRunsTestLeakedIntoTheGate(t *testing.T) {
	h := NewHarness(t, SetupOpts{Agent: "claude", Scenario: configurableFixCommitScenario(t, "guard unsafe value")})
	ctx := context.Background()

	globalConfig := filepath.Join(h.NMHome, "config.yaml")
	globalData, err := os.ReadFile(globalConfig)
	if err != nil {
		t.Fatalf("read global config: %v", err)
	}
	if err := os.WriteFile(globalConfig, []byte(strings.Replace(string(globalData), "  review: 0\n", "  review: 1\n", 1)), 0o644); err != nil {
		t.Fatalf("write global config: %v", err)
	}

	gitGlobal := filepath.Join(h.HomeDir, "gitconfig-isolated")
	for _, kv := range [][2]string{{"user.name", "Owner"}, {"user.email", "owner@example.com"}} {
		if out, err := h.runGit(ctx, h.WorkDir, "config", "--file", gitGlobal, kv[0], kv[1]); err != nil {
			t.Fatalf("write global %s: %v\n%s", kv[0], err, out)
		}
		if out, err := h.runGit(ctx, h.WorkDir, "config", "--local", "--unset", kv[0]); err != nil {
			t.Fatalf("unset checkout %s: %v\n%s", kv[0], err, out)
		}
	}

	h.CommitChange("main", ".no-mistakes.yaml", `ignore_patterns:
  - '*.generated.go'
  - 'vendor/**'
allow_repo_commands: true
commands:
  test: "git config user.name t && git config user.email t@t.t"
`, "run a test command that writes an unscoped git identity")
	if out, err := h.runGit(ctx, h.WorkDir, "push", "origin", "main"); err != nil {
		t.Fatalf("push trusted test config: %v\n%s", err, out)
	}
	if out, err := h.Run("init"); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}

	finishWithOneFixRound := func(branch string) {
		t.Helper()
		h.CommitChange(branch, "feature.txt", "unsafe\n", "add unsafe feature")
		h.PushToGate(branch)
		gated := waitForStepStatus(t, h, branch, types.StepReview, types.StepStatusFixReview, 60*time.Second)
		h.Respond(gated.ID, types.StepReview, types.ActionApprove)
		if run := h.WaitForRun(branch, 90*time.Second); run.Status != types.RunCompleted {
			t.Fatalf("%s run status = %s, want completed (error=%v)", branch, run.Status, deref(run.Error))
		}
	}

	finishWithOneFixRound("feature/leaks-identity")
	gateConfig := filepath.Join(h.NMHome, "repos", h.repoID()+".git", "config")
	leaked, err := h.runGit(ctx, h.WorkDir, "config", "--file", gateConfig, "--get", "user.email")
	if err != nil || strings.TrimSpace(string(leaked)) != "t@t.t" {
		t.Fatalf("precondition: the test command's identity did not reach the shared gate config %s: %q (%v)", gateConfig, leaked, err)
	}

	const branch = "feature/after-the-leak"
	finishWithOneFixRound(branch)
	log, err := h.runGit(ctx, h.UpstreamDir, "log", "--format=%an <%ae>|%cn <%ce>|%s", "main..refs/heads/"+branch)
	if err != nil {
		t.Fatalf("read upstream commits: %v\n%s", err, log)
	}
	pipelineCommits := 0
	for _, line := range strings.Split(strings.TrimSpace(string(log)), "\n") {
		fields := strings.SplitN(line, "|", 3)
		if fields[0] == "E2E Test <e2e@example.com>" {
			continue
		}
		pipelineCommits++
		if fields[0] != "Owner <owner@example.com>" || fields[1] != "Owner <owner@example.com>" {
			t.Errorf("pipeline commit %q is authored %s and committed %s, want the global Owner <owner@example.com>", fields[2], fields[0], fields[1])
		}
	}
	if pipelineCommits == 0 {
		t.Fatalf("no pipeline commit on %s:\n%s", branch, log)
	}
	t.Logf("upstream commits on %s:\n%s", branch, strings.TrimSpace(string(log)))
}
