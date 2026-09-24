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

// TestAxiFreshRunAfterAbortedSubmissionDropsACommit drives a worker through a
// run aborted before push, a branch that then drops one of the submitted
// commits, and a fresh `axi run` on the same branch. The private mirror still
// holds the aborted run's never-published submission, so the fresh submission
// must replace it, archiving the dropped commit instead of refusing the branch.
func TestAxiFreshRunAfterAbortedSubmissionDropsACommit(t *testing.T) {
	h := NewHarness(t, SetupOpts{Agent: "claude", Scenario: branchSyncScenario(t)})
	h.CommitChange("init-dropped", "seed.txt", "seed\n", "seed dropped init")
	initWorktree := h.AddWorktree("init-dropped")
	if out, err := h.RunInDir(initWorktree, "init"); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}

	const branch = "feature/aborted-drop"
	kept := h.CommitChange(branch, "feature.txt", "unsafe\n", "add unsafe feature")
	dropped := h.CommitChange(branch, "dropped.txt", "dropped\n", "add a commit the worker will drop")
	operator := h.AddWorktree(branch)
	gateOut, err := h.RunInDir(operator, "axi", "run", "--intent", "guard the feature before dropping a commit")
	if err != nil || !strings.Contains(gateOut, "sync-1") {
		t.Fatalf("initial review gate: %v\n%s", err, gateOut)
	}
	if out, err := h.RunInDir(operator, "axi", "abort"); err != nil {
		t.Fatalf("axi abort: %v\n%s", err, out)
	}
	aborted := h.WaitForRun(branch, 30*time.Second)
	if aborted.Status != types.RunCancelled {
		t.Fatalf("run status after abort = %s", aborted.Status)
	}

	gateDir := filepath.Join(h.NMHome, "repos", h.repoID()+".git")
	gateHead := func() string {
		t.Helper()
		out, err := h.runGit(context.Background(), gateDir, "rev-parse", "refs/heads/"+branch)
		if err != nil {
			t.Fatalf("read gate head: %v\n%s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	if got := gateHead(); got != dropped {
		t.Fatalf("gate head after abort = %s, want the aborted submission %s", got, dropped)
	}
	if out, err := h.runGit(context.Background(), h.UpstreamDir, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
		t.Fatalf("aborted run published %s upstream", strings.TrimSpace(string(out)))
	}

	if out, err := h.runGit(context.Background(), operator, "reset", "--hard", kept); err != nil {
		t.Fatalf("drop commit: %v\n%s", err, out)
	}
	freshOut, err := h.RunInDir(operator, "axi", "run", "--intent", "revalidate the branch without the dropped commit")
	if err != nil || !strings.Contains(freshOut, "sync-1") {
		t.Fatalf("fresh run after dropping an aborted, unpublished commit was refused: %v\n%s", err, freshOut)
	}
	fresh := h.ActiveRun(branch)
	if fresh == nil || fresh.ID == aborted.ID || fresh.HeadSHA != kept {
		t.Fatalf("fresh run = %+v, want a new run at %s", fresh, kept)
	}
	if got := gateHead(); got != kept {
		t.Fatalf("gate head after fresh submission = %s, want %s", got, kept)
	}
	archive := "refs/tags/no-mistakes-abandoned/" + branch + "/" + dropped
	if out, err := h.runGit(context.Background(), gateDir, "rev-parse", archive); err != nil || strings.TrimSpace(string(out)) != dropped {
		t.Fatalf("dropped commit was not archived at %s: %v\n%s", archive, err, out)
	}
	if out, err := h.RunInDir(operator, "axi", "abort"); err != nil {
		t.Fatalf("cleanup abort: %v\n%s", err, out)
	}
}

// TestAxiFreshRunAfterAbortedSubmissionKeepsRefusalForPublishedContent drives
// the same abort-and-drop journey on a branch the pipeline already published.
// Dropping only the aborted follow-up leaves nothing published at risk, so the
// fresh run starts; dropping the published commit as well keeps the refusal.
func TestAxiFreshRunAfterAbortedSubmissionKeepsRefusalForPublishedContent(t *testing.T) {
	h := NewHarness(t, SetupOpts{Agent: "claude", Scenario: branchSyncScenario(t)})
	h.CommitChange("init-published", "seed.txt", "seed\n", "seed published init")
	initWorktree := h.AddWorktree("init-published")
	if out, err := h.RunInDir(initWorktree, "init"); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}

	const branch = "feature/published-drop"
	published := h.CommitChange(branch, "feature.txt", "unsafe\n", "add published feature")
	operator := h.AddWorktree(branch)
	if out, err := h.RunInDir(operator, "axi", "run", "--intent", "publish the feature"); err != nil || !strings.Contains(out, "sync-1") {
		t.Fatalf("initial review gate: %v\n%s", err, out)
	}
	if out, err := h.RunInDir(operator, "axi", "respond", "--action", "approve"); err != nil || !strings.Contains(out, "outcome: passed") {
		t.Fatalf("publish the feature: %v\n%s", err, out)
	}
	if got := h.UpstreamBranchSHA(branch); got != published {
		t.Fatalf("upstream branch = %s, want the published feature %s", got, published)
	}

	commit := func(path, content, message string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(operator, path), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{"add", path}, {"commit", "-m", message}} {
			if out, err := h.runGit(context.Background(), operator, args...); err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
		}
		return strings.TrimSpace(h.WorktreeRefSHA(branch))
	}
	submitThenAbort := func(intent string) {
		t.Helper()
		if out, err := h.RunInDir(operator, "axi", "run", "--intent", intent); err != nil || !strings.Contains(out, "sync-1") {
			t.Fatalf("review gate for %q: %v\n%s", intent, err, out)
		}
		if out, err := h.RunInDir(operator, "axi", "abort"); err != nil {
			t.Fatalf("axi abort: %v\n%s", err, out)
		}
		if run := h.WaitForRun(branch, 30*time.Second); run.Status != types.RunCancelled {
			t.Fatalf("run status after abort = %s", run.Status)
		}
	}
	reset := func(target string) {
		t.Helper()
		if out, err := h.runGit(context.Background(), operator, "reset", "--hard", target); err != nil {
			t.Fatalf("reset to %s: %v\n%s", target, err, out)
		}
	}

	commit("followup.txt", "follow-up\n", "add follow-up the worker drops")
	submitThenAbort("validate the follow-up")
	reset(published)
	replacement := commit("replacement.txt", "replacement\n", "replace the dropped follow-up")
	submitThenAbort("validate the replacement without the dropped follow-up")

	reset("main")
	commit("rewritten.txt", "rewritten\n", "rewrite without the published feature")
	out, err := h.RunInDir(operator, "axi", "run", "--intent", "drop the published feature")
	if err == nil || !strings.Contains(out, "at-risk commit(s)") || !strings.Contains(out, published) {
		t.Fatalf("dropping published content was not refused by name: %v\n%s", err, out)
	}
	gateDir := filepath.Join(h.NMHome, "repos", h.repoID()+".git")
	if got, gitErr := h.runGit(context.Background(), gateDir, "rev-parse", "refs/heads/"+branch); gitErr != nil || strings.TrimSpace(string(got)) != replacement {
		t.Fatalf("refused submission moved the private mirror to %s (err %v), want %s", strings.TrimSpace(string(got)), gitErr, replacement)
	}
}
