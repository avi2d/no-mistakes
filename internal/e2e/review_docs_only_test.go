//go:build e2e

package e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/types"
)

// The expected outcome comes from the skip rule: a push of only a doc file
// completes Review without its agent, records why, and still publishes.
func TestDocsOnlyPushSkipsTheReviewAgentAndStillPublishes(t *testing.T) {
	h := NewHarness(t, SetupOpts{Agent: "claude", Scenario: cleanReviewScenario(t)})
	if out, err := h.Run("init"); err != nil {
		t.Fatalf("nm init: %v\n%s", err, out)
	}
	head := h.CommitChange("docs-only", "docs/guide.md", "A guide.\n", "add a guide")
	h.PushToGate("docs-only")

	run := h.WaitForRun("docs-only", 60*time.Second)
	if run.Status != types.RunCompleted {
		t.Fatalf("docs-only run did not complete: status=%s error=%v", run.Status, deref(run.Error))
	}
	review, ok := findStep(run.Steps, types.StepReview)
	if !ok || review.Status != types.StepStatusCompleted {
		t.Fatalf("review step = %+v, want completed", review)
	}
	const reason = "only docs and CI schedule lines changed"
	if review.SkipReason != reason {
		t.Fatalf("review skip reason = %q", review.SkipReason)
	}
	logs, err := h.Run("axi", "logs", "--run", run.ID, "--step", "review")
	if err != nil || !strings.Contains(logs, "review agent not run: "+reason) {
		t.Fatalf("review log does not show the skip reason: %v\n%s", err, logs)
	}
	push, ok := findStep(run.Steps, types.StepPush)
	if !ok || push.Status != types.StepStatusCompleted {
		t.Fatalf("push step = %+v, want completed", push)
	}
	if got := h.UpstreamBranchSHA("docs-only"); got != head {
		t.Fatalf("upstream docs-only = %s, want the pushed head %s", got, head)
	}
	if sawPromptContainingAll(h.AgentInvocations(), "Review the code changes", "branch: docs-only") {
		t.Fatal("docs-only review called the agent")
	}
}
