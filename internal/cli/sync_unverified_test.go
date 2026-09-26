package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/custody"
	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/git"
	"github.com/kunchenguid/no-mistakes/internal/paths"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func newCLIUnverifiedRewriteFixture(t *testing.T) cliRecoverFixture {
	t.Helper()
	t.Setenv("NM_HOME", filepath.Join(t.TempDir(), "nm-home"))
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	cliGit(t, root, "init", "--bare", remote)
	local := filepath.Join(root, "operator")
	cliGit(t, root, "init", "-b", "main", local)
	cliGit(t, local, "config", "user.name", "Test")
	cliGit(t, local, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(local, "file.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cliGit(t, local, "add", "file.txt")
	cliGit(t, local, "commit", "-m", "base")
	base := cliGit(t, local, "rev-parse", "HEAD")
	cliGit(t, local, "checkout", "-b", "feature/recover")
	if err := os.WriteFile(filepath.Join(local, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cliGit(t, local, "add", "feature.txt")
	cliGit(t, local, "commit", "-m", "feature")
	submitted := cliGit(t, local, "rev-parse", "HEAD")

	p, err := paths.New()
	if err != nil {
		t.Fatal(err)
	}
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	database, err := db.Open(p.DB())
	if err != nil {
		t.Fatal(err)
	}
	registeredRoot, err := git.FindGitRoot(local)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := database.InsertRepo(registeredRoot, remote, "main")
	if err != nil {
		t.Fatal(err)
	}
	gate := p.RepoDir(repo.ID)
	cliGit(t, filepath.Dir(gate), "init", "--bare", gate)
	cliGit(t, local, "push", gate, "refs/heads/main:refs/heads/main", "refs/heads/feature/recover:refs/heads/feature/recover")

	pipeline := filepath.Join(root, "pipeline")
	cliGit(t, root, "-c", "core.autocrlf=false", "clone", gate, pipeline)
	cliGit(t, pipeline, "config", "user.name", "Pipeline")
	cliGit(t, pipeline, "config", "user.email", "pipeline@example.com")
	cliGit(t, pipeline, "checkout", "main")
	if err := os.WriteFile(filepath.Join(pipeline, "CHANGELOG.md"), []byte("newly merged PR\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cliGit(t, pipeline, "add", "CHANGELOG.md")
	cliGit(t, pipeline, "commit", "-m", "newly merged PR")
	cliGit(t, pipeline, "push", "origin", "main")
	cliGit(t, pipeline, "checkout", "--detach", submitted)
	cliGit(t, pipeline, "merge", "--no-ff", "--no-edit", "origin/main")
	recorded := cliGit(t, pipeline, "rev-parse", "HEAD")
	cliGit(t, pipeline, "rebase", "origin/main")
	rewritten := cliGit(t, pipeline, "rev-parse", "HEAD")
	cliGit(t, pipeline, "push", "origin", recorded+":refs/no-mistakes/test-recorded", rewritten+":refs/no-mistakes/test-rewritten")
	cliGit(t, gate, "update-ref", "-d", "refs/no-mistakes/test-recorded")
	cliGit(t, gate, "update-ref", "-d", "refs/no-mistakes/test-rewritten")

	run, err := database.InsertRun(repo.ID, "feature/recover", submitted, base)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.UpdateRunHeadSHA(run.ID, recorded); err != nil {
		t.Fatal(err)
	}
	if err := database.UpdateRunErrorStatus(run.ID, "refusing to run review step: worktree HEAD "+rewritten+" is not a descendant of the pipeline's recorded head "+recorded, types.RunFailed); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	chdir(t, local)
	return cliRecoverFixture{
		local: local, gate: gate, remote: remote, base: base, submitted: submitted,
		preserved: recorded, runID: run.ID, repoID: repo.ID,
	}
}

func TestAxiSyncRecoverKeepLocalReturnsCustodyForUnverifiedTerminalHead(t *testing.T) {
	f := newCLIUnverifiedRewriteFixture(t)

	status, err := executeCmd("axi", "status")
	var ee *exitError
	if err != nil && (!asExitError(err, &ee) || ee.code != 1) {
		t.Fatalf("axi status: %v\n%s", err, status)
	}
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

	refused, err := executeCmd("axi", "sync", "--recover")
	if err == nil || !asExitError(err, &ee) || ee.code != 1 {
		t.Fatalf("plain recover of an unverified head should refuse, got %#v\n%s", err, refused)
	}
	if !strings.Contains(refused, "safety: blocked_recover_unverified_head") {
		t.Fatalf("plain recover refusal:\n%s", refused)
	}

	kept, err := executeCmd("axi", "sync", "--recover", "--keep-local")
	if err != nil {
		t.Fatalf("keep-local recover of an unverified head: %v\n%s", err, kept)
	}
	for _, want := range []string{"recovered: true", "state: custody_returned", "changed: false"} {
		if !strings.Contains(kept, want) {
			t.Errorf("keep-local output missing %q:\n%s", want, kept)
		}
	}
	if got := cliGit(t, f.local, "rev-parse", "HEAD"); got != f.submitted {
		t.Fatalf("keep-local moved HEAD to %s, want %s", got, f.submitted)
	}
	if got := cliGit(t, f.gate, "rev-parse", custody.RecoveryRef(f.runID)); got != f.preserved {
		t.Fatalf("gate recovery anchor = %s, want the recorded pipeline head %s", got, f.preserved)
	}
	if stamped, total := cliRecoverRunCustodyStamps(t, f.runID); stamped != 1 || total != 1 {
		t.Fatalf("custody stamps = %d/%d, want 1/1", stamped, total)
	}

	after, err := executeCmd("axi", "status")
	if err != nil {
		t.Fatalf("post-recover axi status: %v\n%s", err, after)
	}
	if !strings.Contains(after, "state: custody_returned") || !strings.Contains(after, "no-mistakes axi run --intent") {
		t.Fatalf("post-recover status should unblock axi run:\n%s", after)
	}
	t.Logf("axi status before recovery:\n%s\nkeep-local recovery:\n%s\naxi status after recovery:\n%s", status, kept, after)
}

func TestAxiSyncBindArchiveStillRefusesUnverifiedTerminalHead(t *testing.T) {
	f := newCLIUnverifiedRewriteFixture(t)
	archiveRef := "refs/heads/archive/rewrite-" + f.runID
	cliGit(t, f.local, "fetch", "--no-tags", f.gate, f.submitted+":"+archiveRef)

	out, err := executeCmd("axi", "sync", "--bind-archive-ref", archiveRef)
	var ee *exitError
	if err == nil || !asExitError(err, &ee) || ee.code != 1 {
		t.Fatalf("binding an archive to an unverified head should refuse, got %#v\n%s", err, out)
	}
	if !strings.Contains(out, "blocked_recover_archive_unverified_head") {
		t.Fatalf("archive refusal:\n%s", out)
	}
	p, err := paths.New()
	if err != nil {
		t.Fatal(err)
	}
	database, err := db.Open(p.DB())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	records, err := database.GetRecoveryArchivesByRun(f.runID)
	if err != nil || len(records) != 0 {
		t.Fatalf("refused archive bind left records %#v (err %v)", records, err)
	}
}
