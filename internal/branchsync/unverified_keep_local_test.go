package branchsync

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/custody"
	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func newUnverifiedRewriteFixture(t *testing.T) (*recoverFixture, string) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	remote := filepath.Join(root, "upstream.git")
	mustRun(t, root, "init", "--bare", remote)
	local := filepath.Join(root, "operator")
	mustRun(t, root, "init", "-b", "main", local)
	configureIdentity(t, local)
	mustWrite(t, filepath.Join(local, "file.txt"), "base\n")
	mustRun(t, local, "add", "file.txt")
	mustRun(t, local, "commit", "-m", "base")
	base := mustRun(t, local, "rev-parse", "HEAD")
	mustRun(t, local, "checkout", "-b", "feature/recover")
	mustWrite(t, filepath.Join(local, "feature.txt"), "feature\n")
	mustRun(t, local, "add", "feature.txt")
	mustRun(t, local, "commit", "-m", "feature")
	submitted := mustRun(t, local, "rev-parse", "HEAD")

	gate := filepath.Join(root, "gate.git")
	mustRun(t, root, "init", "--bare", gate)
	mustRun(t, local, "push", gate, "refs/heads/main:refs/heads/main", "refs/heads/feature/recover:refs/heads/feature/recover")

	pipeline := filepath.Join(root, "pipeline")
	mustRun(t, root, "-c", "core.autocrlf=false", "clone", gate, pipeline)
	configureIdentity(t, pipeline)
	mustRun(t, pipeline, "checkout", "main")
	mustWrite(t, filepath.Join(pipeline, "CHANGELOG.md"), "newly merged PR\n")
	mustRun(t, pipeline, "add", "CHANGELOG.md")
	mustRun(t, pipeline, "commit", "-m", "newly merged PR")
	mustRun(t, pipeline, "push", "origin", "main")
	mustRun(t, pipeline, "checkout", "--detach", submitted)
	mustRun(t, pipeline, "merge", "--no-ff", "--no-edit", "origin/main")
	recorded := mustRun(t, pipeline, "rev-parse", "HEAD")
	mustRun(t, pipeline, "rebase", "origin/main")
	rewritten := mustRun(t, pipeline, "rev-parse", "HEAD")
	mustRun(t, pipeline, "push", "origin", recorded+":refs/no-mistakes/test-recorded", rewritten+":refs/no-mistakes/test-rewritten")
	mustRun(t, gate, "update-ref", "-d", "refs/no-mistakes/test-recorded")
	mustRun(t, gate, "update-ref", "-d", "refs/no-mistakes/test-rewritten")

	database, err := db.Open(filepath.Join(root, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	repo, err := database.InsertRepo(local, remote, "main")
	if err != nil {
		t.Fatal(err)
	}
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
	run, _ = database.GetRun(run.ID)
	return &recoverFixture{
		t: t, ctx: ctx, db: database, repo: repo, run: run,
		service: &Service{DB: database, Repo: repo, WorkDir: local, GateDir: gate},
		local:   local, gate: gate, remote: remote,
		base: base, submitted: submitted, preserved: recorded,
	}, rewritten
}

func assertKeepLocalOffer(t *testing.T, state State) {
	t.Helper()
	if state.State != StatePipelineOwned || state.Safety != "blocked_recover_unverified_head" {
		t.Errorf("unverified terminal head state = %s / %s: %s", state.State, state.Safety, state.Error)
	}
	if state.NextAction == nil || state.NextAction.Code != "recover_custody" || state.NextAction.Command != "no-mistakes axi sync --recover --keep-local" {
		t.Errorf("unverified terminal head next action = %#v, want the keep-local custody return", state.NextAction)
	}
}

func TestRecoverKeepLocalReturnsCustodyForUnverifiedTerminalHead(t *testing.T) {
	t.Parallel()

	f, _ := newUnverifiedRewriteFixture(t)
	if f.run.TerminalHeadVerifiedAt != nil {
		t.Fatal("fixture terminal head is verified")
	}
	if !objectExists(f.ctx, f.gate, f.preserved) || objectExists(f.ctx, f.local, f.preserved) {
		t.Fatal("fixture recorded head must exist only in the gate")
	}
	remoteBefore := mustRun(t, f.remote, "for-each-ref", "--format=%(refname) %(objectname)")

	assertKeepLocalOffer(t, f.service.InspectCached(f.ctx))
	if plain := assertUnverifiedRecoveryRefusalNoMutation(t, f); plain.Safety != "blocked_recover_unverified_head" {
		t.Fatalf("plain recovery of an unverified head = %s, want the existing refusal", plain.Safety)
	}

	kept := f.service.Recover(f.ctx, true)
	if !kept.Recovered || kept.Changed || kept.State != StateCustodyReturned {
		t.Fatalf("keep-local recovery of an unverified head = %#v", kept)
	}
	if got := mustRun(t, f.local, "rev-parse", "HEAD"); got != f.submitted {
		t.Fatalf("operator HEAD = %s, want the kept local head %s", got, f.submitted)
	}
	if clean, reason := worktreeClean(f.ctx, f.local); !clean {
		t.Fatalf("keep-local touched the worktree: %s", reason)
	}
	if got := mustRun(t, f.gate, "rev-parse", "refs/heads/feature/recover"); got != f.submitted {
		t.Fatalf("gate branch = %s, want the kept local head %s", got, f.submitted)
	}
	if got := mustRun(t, f.gate, "rev-parse", custody.RecoveryRef(f.run.ID)); got != f.preserved {
		t.Fatalf("gate recovery anchor = %s, want the recorded pipeline head %s", got, f.preserved)
	}
	if !f.custodyReturned() {
		t.Fatal("keep-local did not stamp custody")
	}
	if again := f.service.Recover(f.ctx, true); !again.Recovered || again.Changed {
		t.Fatalf("repeated keep-local recovery = %#v", again)
	}
	if got := mustRun(t, f.remote, "for-each-ref", "--format=%(refname) %(objectname)"); got != remoteBefore {
		t.Fatalf("recovery changed the push target: %q != %q", got, remoteBefore)
	}
}

func TestRecoverKeepLocalPreservesAMovedGateHeadForUnverifiedTerminalHead(t *testing.T) {
	t.Parallel()

	f, rewritten := newUnverifiedRewriteFixture(t)
	mustRun(t, f.gate, "update-ref", "refs/heads/feature/recover", rewritten, f.submitted)

	assertKeepLocalOffer(t, f.service.InspectCached(f.ctx))
	kept := f.service.Recover(f.ctx, true)
	if !kept.Recovered || kept.Changed {
		t.Fatalf("keep-local recovery over a moved gate head = %#v", kept)
	}
	if got := mustRun(t, f.gate, "rev-parse", custody.RecoveryGateRef(f.run.ID)); got != rewritten {
		t.Fatalf("gate head anchor = %s, want the moved gate head %s", got, rewritten)
	}
	if got := mustRun(t, f.gate, "rev-parse", custody.RecoveryRef(f.run.ID)); got != f.preserved {
		t.Fatalf("gate recovery anchor = %s, want the recorded pipeline head %s", got, f.preserved)
	}
	if got := mustRun(t, f.gate, "rev-parse", "refs/heads/feature/recover"); got != f.submitted {
		t.Fatalf("gate branch = %s, want the kept local head %s", got, f.submitted)
	}
}

func TestRecoverKeepLocalRefusesUnverifiedTerminalHeadWithConflictingAnchor(t *testing.T) {
	t.Parallel()

	f, rewritten := newUnverifiedRewriteFixture(t)
	mustRun(t, f.gate, "update-ref", custody.RecoveryRef(f.run.ID), rewritten)
	gateRefsBefore := mustRun(t, f.gate, "for-each-ref", "--format=%(refname) %(objectname)")

	assertManualReconciliationOffer(t, f.service.InspectCached(f.ctx))
	kept := f.service.Recover(f.ctx, true)
	if kept.Recovered || kept.Changed {
		t.Fatalf("keep-local over conflicting recovery evidence succeeded: %#v", kept)
	}
	if f.custodyReturned() {
		t.Fatal("conflicting recovery evidence stamped custody")
	}
	if got := mustRun(t, f.gate, "for-each-ref", "--format=%(refname) %(objectname)"); got != gateRefsBefore {
		t.Fatalf("refusal changed gate refs: %q != %q", got, gateRefsBefore)
	}
}
