package steps

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/branchsync"
	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

// commitIdentities reads the author and committer of rev the way the forge
// reports them, so the oracle for every identity test below is the git log
// itself rather than the code under test.
func commitIdentities(t *testing.T, dir, rev string) (author, committer string) {
	t.Helper()
	out := gitCmd(t, dir, "log", "-1", "--format=%an%n%ae%n%cn%n%ce", rev)
	parts := strings.Split(out, "\n")
	if len(parts) != 4 {
		t.Fatalf("identity fields for %s = %q, want four lines", rev, out)
	}
	return parts[0] + " <" + parts[1] + ">", parts[2] + " <" + parts[3] + ">"
}

// rogueGit runs git with the fixer agent's foreign identity from the orc
// incident: no-mistakes <no-mistakes@local>. The identity rides the
// environment rather than -c flags, because git reads GIT_AUTHOR_* and
// GIT_COMMITTER_* ahead of the config file the -c flags override, which is
// also why the test git helper's own test identity cannot stand in for it.
func rogueGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=no-mistakes",
		"GIT_AUTHOR_EMAIL=no-mistakes@local",
		"GIT_COMMITTER_NAME=no-mistakes",
		"GIT_COMMITTER_EMAIL=no-mistakes@local",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// rogueCommit advances dir the way the orc fixer agent did: the agent
// committed its own repair as no-mistakes <no-mistakes@local>.
func rogueCommit(t *testing.T, dir, name, content string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	rogueGit(t, dir, "add", "-A")
	rogueGit(t, dir, "commit", "-m", "agent repair")
	return gitCmd(t, dir, "rev-parse", "HEAD")
}
func TestCIStep_CommitRepair_RestampsForeignAgentCommit(t *testing.T) {
	t.Parallel()
	upstream := t.TempDir()
	gitCmd(t, upstream, "init", "--bare")

	dir := t.TempDir()
	gitCmd(t, dir, "init")
	gitCmd(t, dir, "config", "user.name", "test")
	gitCmd(t, dir, "config", "user.email", "test@test.com")
	gitCmd(t, dir, "checkout", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "init.txt"), []byte("init"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-m", "initial")
	baseSHA := gitCmd(t, dir, "rev-parse", "HEAD")
	gitCmd(t, dir, "remote", "add", "origin", upstream)
	gitCmd(t, dir, "push", "origin", "main")

	gitCmd(t, dir, "checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(dir, "feature.txt"), []byte("feature"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-m", "feature")
	recordedSHA := gitCmd(t, dir, "rev-parse", "HEAD")
	gitCmd(t, dir, "push", "origin", "feature")

	foreignSHA := rogueCommit(t, dir, "agentfix.txt", "agent fix")

	ag := &mockAgent{name: "test"}
	sctx := newTestContextWithDBRecords(t, ag, dir, baseSHA, recordedSHA, config.Commands{})
	sctx.Repo.UpstreamURL = upstream
	sctx.Run.Branch = "refs/heads/feature"
	if err := sctx.DB.UpdateRunPushBinding(sctx.Run.ID, db.PushBinding{HeadSHA: recordedSHA, TargetKind: "upstream", TargetFingerprint: branchsync.TargetFingerprint(upstream), Ref: "refs/heads/feature"}); err != nil {
		t.Fatal(err)
	}
	sctx.Config.CI.RevalidateRepairs = true
	repair, err := (&CIStep{}).commitRepair(sctx, "fix the typecheck", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !repair.HeadAdvanced {
		t.Fatal("expected the restamped repair to advance the head")
	}

	newHead := gitCmd(t, dir, "rev-parse", "HEAD")
	if newHead == foreignSHA {
		t.Fatalf("adopted the foreign agent commit %s instead of restamping it", foreignSHA)
	}
	author, committer := commitIdentities(t, dir, newHead)
	if author != "test <test@test.com>" || committer != "test <test@test.com>" {
		t.Fatalf("repaired commit identity = %s / %s, want test <test@test.com>", author, committer)
	}
	if got := gitCmd(t, dir, "log", "-1", "--format=%s"); got != "no-mistakes(ci): fix the typecheck" {
		t.Fatalf("repaired commit subject = %q", got)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "agentfix.txt")); err != nil || string(got) != "agent fix" {
		t.Fatalf("repaired tree lost the agent fix: %q, %v", got, err)
	}
	if sctx.Run.HeadSHA != newHead {
		t.Fatalf("Run.HeadSHA = %s, want %s", sctx.Run.HeadSHA, newHead)
	}
	dbRun, err := sctx.DB.GetRun(sctx.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if dbRun.HeadSHA != newHead {
		t.Fatalf("DB HeadSHA = %s, want %s", dbRun.HeadSHA, newHead)
	}
	if upstreamSHA := gitCmd(t, upstream, "rev-parse", "refs/heads/feature"); upstreamSHA != recordedSHA {
		t.Fatalf("upstream head = %s, want held head %s", upstreamSHA, recordedSHA)
	}
}

func TestCommitAgentFixes_RestampsForeignAgentCommit(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)
	sctx := newTestContextWithDBRecords(t, &mockAgent{name: "claude"}, dir, baseSHA, headSHA, config.Commands{})

	foreignSHA := rogueCommit(t, dir, "agent-change.txt", "change")

	committed, err := commitAgentFixesWithResult(sctx, types.StepReview, "fix review findings", "fallback", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !committed {
		t.Fatal("expected the restamped fix to commit")
	}
	newHead := gitCmd(t, dir, "rev-parse", "HEAD")
	if newHead == foreignSHA {
		t.Fatalf("adopted the foreign agent commit %s instead of restamping it", foreignSHA)
	}
	author, committer := commitIdentities(t, dir, newHead)
	if author != "test <test@test.com>" || committer != "test <test@test.com>" {
		t.Fatalf("fix commit identity = %s / %s, want test <test@test.com>", author, committer)
	}
	if got := lastCommitMessage(t, dir); got != "no-mistakes(review): fix review findings" {
		t.Fatalf("fix commit subject = %q", got)
	}
	if got := gitCmd(t, dir, "show", newHead+":agent-change.txt"); got != "change" {
		t.Fatalf("repaired tree lost the agent fix: %q", got)
	}
	if sctx.Run.HeadSHA != newHead {
		t.Fatalf("Run.HeadSHA = %s, want %s", sctx.Run.HeadSHA, newHead)
	}
}
func TestCommitPipelineCorrection_PinnedIdentityBeatsAmbientEnv(t *testing.T) {
	dir, _, _ := setupGitRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "pinned.txt"), []byte("pinned\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "add", "pinned.txt")

	t.Setenv("GIT_AUTHOR_NAME", "rogue")
	t.Setenv("GIT_AUTHOR_EMAIL", "rogue@local")
	t.Setenv("GIT_COMMITTER_NAME", "rogue")
	t.Setenv("GIT_COMMITTER_EMAIL", "rogue@local")
	sctx := newTestContext(t, &mockAgent{name: "test"}, dir, "", "", config.Commands{})
	if err := commitPipelineCorrection(sctx, dir, "no-mistakes(test): pinned", nil); err != nil {
		t.Fatal(err)
	}
	author, committer := commitIdentities(t, dir, "HEAD")
	if author != "test <test@test.com>" || committer != "test <test@test.com>" {
		t.Fatalf("pinned commit identity = %s / %s, want test <test@test.com>", author, committer)
	}
}

func TestNormalizeAgentHead_AdoptsDomesticAgentCommitUnchanged(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)
	sctx := newTestContext(t, &mockAgent{name: "test"}, dir, baseSHA, headSHA, config.Commands{})

	if err := os.WriteFile(filepath.Join(dir, "domestic.txt"), []byte("domestic"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-m", "agent fix")
	agentSHA := gitCmd(t, dir, "rev-parse", "HEAD")

	if err := normalizeAgentHead(sctx); err != nil {
		t.Fatal(err)
	}
	if got := gitCmd(t, dir, "rev-parse", "HEAD"); got != agentSHA {
		t.Fatalf("rewrote a domestic agent commit %s to %s", agentSHA, got)
	}
}

func TestNormalizeAgentHead_RefusesForeignMergeConclusion(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)
	sctx := newTestContext(t, &mockAgent{name: "test"}, dir, baseSHA, headSHA, config.Commands{})
	branch := gitCmd(t, dir, "rev-parse", "--abbrev-ref", "HEAD")

	gitCmd(t, dir, "checkout", "-b", "side")
	if err := os.WriteFile(filepath.Join(dir, "side.txt"), []byte("side"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-m", "side work")
	gitCmd(t, dir, "checkout", branch)
	rogueGit(t, dir, "merge", "--no-ff", "--no-edit", "side")
	mergeSHA := gitCmd(t, dir, "rev-parse", "HEAD")

	if err := normalizeAgentHead(sctx); err == nil {
		t.Fatal("expected the gate to refuse a foreign merge conclusion")
	} else if !strings.Contains(err.Error(), "foreign identity") {
		t.Fatalf("refusal = %q, want it to name the foreign identity", err)
	}
	if got := gitCmd(t, dir, "rev-parse", "HEAD"); got != mergeSHA {
		t.Fatalf("gate moved HEAD %s to %s while refusing", mergeSHA, got)
	}
}
func TestNormalizeAgentHead_ReauthorsForeignTipReplacement(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)
	sctx := newTestContext(t, &mockAgent{name: "test"}, dir, baseSHA, headSHA, config.Commands{})

	if err := os.WriteFile(filepath.Join(dir, "amended.txt"), []byte("amended"), 0o644); err != nil {
		t.Fatal(err)
	}
	rogueGit(t, dir, "add", "-A")
	rogueGit(t, dir, "commit", "--amend", "--no-edit")
	amendedSHA := gitCmd(t, dir, "rev-parse", "HEAD")
	amendedParent := gitCmd(t, dir, "rev-parse", "HEAD^")

	if err := normalizeAgentHead(sctx); err != nil {
		t.Fatal(err)
	}
	newHead := gitCmd(t, dir, "rev-parse", "HEAD")
	if newHead == amendedSHA {
		t.Fatalf("adopted the foreign amended head %s instead of re-authoring it", amendedSHA)
	}
	if got := gitCmd(t, dir, "rev-parse", newHead+"^"); got != amendedParent {
		t.Fatalf("re-authored head has parent %s, want the rewrite's parent %s", got, amendedParent)
	}
	author, committer := commitIdentities(t, dir, newHead)
	if author != "test <test@test.com>" || committer != "test <test@test.com>" {
		t.Fatalf("re-authored head identity = %s / %s, want test <test@test.com>", author, committer)
	}
	if got := gitCmd(t, dir, "show", newHead+":amended.txt"); got != "amended" {
		t.Fatalf("re-authored head lost the fix: %q", got)
	}
}
