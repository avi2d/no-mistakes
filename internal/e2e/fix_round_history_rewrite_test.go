//go:build e2e

package e2e

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/custody"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func fixRoundHistoryRewriteScenario(t *testing.T, fixGit string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fix-round-history-rewrite-scenario.yaml")
	content := `actions:
  - match: "Investigate previous review findings"
    text: "rebased onto main to pick up the newly merged PR"
    git:
` + fixGit + `
    structured:
      summary: "regenerate changelog"
  - match: "Review the code changes and return structured findings"
    text: "review found a stale changelog"
    structured:
      findings:
        - id: "changelog-1"
          severity: warning
          file: "CHANGELOG.md"
          line: 1
          description: "regenerate CHANGELOG.md for the newly merged PR"
          action: auto-fix
      summary: "found one issue"
      risk_level: medium
      risk_rationale: "the changelog misses the newly merged PR"
      risk_scope: source-or-external
  - text: "no issues found"
    structured:
      findings: []
      summary: "no issues found"
      risk_level: low
      risk_rationale: "no remaining risk"
      risk_scope: source-or-external
      tested: ["fakeagent: focused verification"]
      testing_summary: "simulated tests passed"
      scenarios:
        - name: "fakeagent: simulated end-to-end scenario"
          result: pass
          live: true
          evidence: "fakeagent: simulated test run"
          reason: ""
      verdict: go
      artifacts: []
      title: "feat: changelog entry"
      body: "fix-round history rewrite journey"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write fix-round history rewrite scenario: %v", err)
	}
	return path
}

func TestFixRoundHistoryRewriteJourney(t *testing.T) {
	h := NewHarness(t, SetupOpts{Agent: "claude", Scenario: fixRoundHistoryRewriteScenario(t, `      - ["rebase", "origin/main"]`)})
	commitTrustedRepoConfig(t, h, "rebase:\n  strategy: merge\n")
	initFromOwnWorktree(t, h, "init-fix-round-rewrite")

	const branch = "feature/fix-round-rewrite"
	submitted := h.CommitChange(branch, "CHANGELOG.md", "- add the feature\n", "add the feature")
	movedMain := advanceMain(t, h, "merged-pr.txt", "newly merged PR\n", "merge an unrelated PR")
	operator := h.AddWorktree(branch)
	gateDir := filepath.Join(h.NMHome, "repos", h.repoID()+".git")

	gateOut, err := h.RunInDir(operator, "axi", "run", "--intent", "add the feature changelog entry")
	if err != nil || !strings.Contains(gateOut, "changelog-1") {
		t.Fatalf("review gate: %v\n%s", err, gateOut)
	}
	fixOut, _ := h.RunInDir(operator, "axi", "respond", "--action", "fix", "--findings", "changelog-1")
	t.Logf("fix response:\n%s", fixOut)

	run := h.WaitForRun(branch, 60*time.Second)
	if run.Status != types.RunFailed {
		t.Fatalf("run after a history-rewriting fix round = %s (error %q), want failed", run.Status, deref(run.Error))
	}
	for _, want := range []string{"review fix round rewrote branch history", "never rebase, reset, or amend", run.HeadSHA} {
		if !strings.Contains(deref(run.Error), want) {
			t.Errorf("run error does not mention %q: %s", want, deref(run.Error))
		}
	}
	if strings.Contains(deref(run.Error), "out-of-band") {
		t.Errorf("run error blames an out-of-band writer: %s", deref(run.Error))
	}
	if got := parentsOf(t, h, gateDir, run.HeadSHA); len(got) != 2 || got[0] != submitted || got[1] != movedMain {
		t.Fatalf("recorded head %s parents = %v, want the rebase step's merge of [%s %s]", run.HeadSHA, got, submitted, movedMain)
	}
	if got := gitIn(t, h, gateDir, "rev-parse", custody.RecoveryRef(run.ID)); got != run.HeadSHA {
		t.Fatalf("terminal recovery anchor = %s, want the verified recorded head %s", got, run.HeadSHA)
	}

	status, _ := h.RunInDir(operator, "axi", "status")
	for _, want := range []string{"state: pipeline_owned", "code: recover_custody"} {
		if !strings.Contains(status, want) {
			t.Errorf("stranded status missing %q:\n%s", want, status)
		}
	}
	recovered, err := h.RunInDir(operator, "axi", "sync", "--recover", "--keep-local")
	if err != nil {
		t.Fatalf("keep-local custody recovery: %v\n%s", err, recovered)
	}
	for _, want := range []string{"recovered: true", "state: custody_returned"} {
		if !strings.Contains(recovered, want) {
			t.Errorf("recovery output missing %q:\n%s", want, recovered)
		}
	}
	if got, gitErr := h.runGit(context.Background(), operator, "rev-parse", "HEAD"); gitErr != nil || strings.TrimSpace(string(got)) != submitted {
		t.Fatalf("operator HEAD after keep-local = %s (err %v), want %s", strings.TrimSpace(string(got)), gitErr, submitted)
	}

	fresh, err := h.RunInDir(operator, "axi", "run", "--intent", "add the feature changelog entry")
	if err != nil || !strings.Contains(fresh, "gate:") {
		t.Fatalf("fresh run after custody recovery: %v\n%s", err, fresh)
	}
	t.Logf("status before recovery:\n%s\nrecovery:\n%s\nfresh run:\n%s", status, recovered, fresh)
}

func TestFixAgentFailureAfterHistoryRewriteJourney(t *testing.T) {
	h := NewHarness(t, SetupOpts{Agent: "claude", Scenario: fixRoundHistoryRewriteScenario(t,
		"      - [\"rebase\", \"origin/main\"]\n      - [\"rev-parse\", \"--verify\", \"refs/heads/fix-agent-crashes-here\"]")})
	commitTrustedRepoConfig(t, h, "rebase:\n  strategy: merge\n")
	initFromOwnWorktree(t, h, "init-fix-agent-failure")

	const branch = "feature/fix-agent-failure"
	submitted := h.CommitChange(branch, "CHANGELOG.md", "- add the feature\n", "add the feature")
	advanceMain(t, h, "merged-pr.txt", "newly merged PR\n", "merge an unrelated PR")
	operator := h.AddWorktree(branch)
	gateDir := filepath.Join(h.NMHome, "repos", h.repoID()+".git")

	gateOut, err := h.RunInDir(operator, "axi", "run", "--intent", "add the feature changelog entry")
	if err != nil || !strings.Contains(gateOut, "changelog-1") {
		t.Fatalf("review gate: %v\n%s", err, gateOut)
	}
	fixOut, _ := h.RunInDir(operator, "axi", "respond", "--action", "fix", "--findings", "changelog-1")
	t.Logf("fix response:\n%s", fixOut)
	run := h.WaitForRun(branch, 60*time.Second)
	if run.Status != types.RunFailed {
		t.Fatalf("run after a failed fix agent = %s (error %q), want failed", run.Status, deref(run.Error))
	}
	if _, err := h.runGit(context.Background(), gateDir, "rev-parse", "--verify", custody.RecoveryRef(run.ID)); err == nil {
		t.Fatal("terminalization pinned a head it could not verify")
	}

	status, _ := h.RunInDir(operator, "axi", "status")
	for _, want := range []string{
		"state: pipeline_owned",
		"safety: blocked_recover_unverified_head",
		"code: recover_custody",
		"command: no-mistakes axi sync --recover --keep-local",
	} {
		if !strings.Contains(status, want) {
			t.Errorf("unverified-head status missing %q:\n%s", want, status)
		}
	}
	refused, err := h.RunInDir(operator, "axi", "sync", "--recover")
	if err == nil {
		t.Fatalf("plain recovery of an unverified head should refuse:\n%s", refused)
	}
	for _, want := range []string{"safety: blocked_recover_unverified_head", "command: no-mistakes axi sync --recover --keep-local"} {
		if !strings.Contains(refused, want) {
			t.Errorf("plain recovery refusal missing %q:\n%s", want, refused)
		}
	}
	recovered, err := h.RunInDir(operator, "axi", "sync", "--recover", "--keep-local")
	if err != nil {
		t.Fatalf("keep-local custody recovery of an unverified head: %v\n%s", err, recovered)
	}
	for _, want := range []string{"recovered: true", "state: custody_returned"} {
		if !strings.Contains(recovered, want) {
			t.Errorf("recovery output missing %q:\n%s", want, recovered)
		}
	}
	if got, gitErr := h.runGit(context.Background(), operator, "rev-parse", "HEAD"); gitErr != nil || strings.TrimSpace(string(got)) != submitted {
		t.Fatalf("operator HEAD after keep-local = %s (err %v), want %s", strings.TrimSpace(string(got)), gitErr, submitted)
	}
	if got := gitIn(t, h, gateDir, "rev-parse", custody.RecoveryRef(run.ID)); got != run.HeadSHA {
		t.Fatalf("recovery anchor = %s, want the recorded pipeline head %s", got, run.HeadSHA)
	}

	fresh, err := h.RunInDir(operator, "axi", "run", "--intent", "add the feature changelog entry")
	if err != nil || !strings.Contains(fresh, "gate:") {
		t.Fatalf("fresh run after custody recovery: %v\n%s", err, fresh)
	}
	t.Logf("status before recovery:\n%s\nrefused plain recovery:\n%s\nkeep-local recovery:\n%s", status, refused, recovered)
}
