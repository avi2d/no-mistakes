package gate

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// abandonedFixture is a private mirror left at an aborted run's submission
// (base -> published -> abandoned) and a caller's repository holding both
// commits, from which a test cuts the live head the fresh submission pushes.
type abandonedFixture struct {
	work, gateDir                string
	base, published, abandonedAt string
}

func newAbandonedFixture(t *testing.T) abandonedFixture {
	t.Helper()
	work := initReconcileRepo(t)
	f := abandonedFixture{work: work, base: reconcileGit(t, work, "rev-parse", "HEAD")}
	writeReconcileFile(t, work, "published.txt", "published work\n")
	reconcileGit(t, work, "add", "published.txt")
	reconcileGit(t, work, "commit", "-m", "published work")
	f.published = reconcileGit(t, work, "rev-parse", "HEAD")
	writeReconcileFile(t, work, "dropped.txt", "never published\n")
	reconcileGit(t, work, "add", "dropped.txt")
	reconcileGit(t, work, "commit", "-m", "commit the worker drops")
	f.abandonedAt = reconcileGit(t, work, "rev-parse", "HEAD")

	f.gateDir = filepath.Join(t.TempDir(), "gate.git")
	reconcileGit(t, "", "init", "--bare", f.gateDir)
	reconcileGit(t, f.gateDir, "fetch", work, f.abandonedAt+":refs/heads/feature")
	return f
}

func (f abandonedFixture) liveOn(t *testing.T, parent string) string {
	t.Helper()
	reconcileGit(t, f.work, "reset", "--hard", parent)
	writeReconcileFile(t, f.work, "live.txt", "fresh work\n")
	reconcileGit(t, f.work, "add", "live.txt")
	reconcileGit(t, f.work, "commit", "-m", "fresh work")
	return reconcileGit(t, f.work, "rev-parse", "HEAD")
}

func (f abandonedFixture) assertUntouched(t *testing.T) {
	t.Helper()
	if got := reconcileGit(t, f.gateDir, "rev-parse", "refs/heads/feature"); got != f.abandonedAt {
		t.Fatalf("refusal moved the private mirror to %s, want %s", got, f.abandonedAt)
	}
	if tags := reconcileGit(t, f.gateDir, "tag", "--list", "no-mistakes-abandoned/*"); tags != "" {
		t.Fatalf("refusal archived the private mirror: %q", tags)
	}
}

func TestReconcileStaleBranchReplacesAbandonedSubmissionThatDroppedACommit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newAbandonedFixture(t)
	reconcileGit(t, f.work, "reset", "--hard", f.published)
	liveHead := f.published

	if _, err := ReconcileStaleBranch(ctx, f.gateDir, f.work, "feature", liveHead, AbandonedSubmission{}); err == nil || !strings.Contains(err.Error(), f.abandonedAt) {
		t.Fatalf("dropped commit without an abandoned submission: err=%v, want a refusal naming %s", err, f.abandonedAt)
	}
	f.assertUntouched(t)

	result, err := ReconcileStaleBranch(ctx, f.gateDir, f.work, "feature", liveHead, AbandonedSubmission{Head: f.abandonedAt})
	if err != nil {
		t.Fatalf("abandoned, never-published submission was refused: %v", err)
	}
	if !result.Reconciled || result.PreviousHead != f.abandonedAt {
		t.Fatalf("reconciliation result = %+v", result)
	}
	if got := reconcileGit(t, f.gateDir, "rev-parse", result.ArchivedTag+"^{commit}"); got != f.abandonedAt {
		t.Fatalf("abandoned submission was removed without an archive: %s", got)
	}
	reconcileGit(t, f.work, "push", f.gateDir, liveHead+":refs/heads/feature")
}

func TestReconcileStaleBranchAbandonedSubmissionKeepsRefusalForPublishedContent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("dropping a published ancestor", func(t *testing.T) {
		t.Parallel()
		f := newAbandonedFixture(t)
		liveHead := f.liveOn(t, f.base)
		_, err := ReconcileStaleBranch(ctx, f.gateDir, f.work, "feature", liveHead, AbandonedSubmission{Head: f.abandonedAt, Publications: []string{f.published}})
		if err == nil || !strings.Contains(err.Error(), f.published) || !strings.Contains(err.Error(), "published work") {
			t.Fatalf("published ancestor dropped under the abandoned-submission exception: err=%v", err)
		}
		f.assertUntouched(t)
	})

	t.Run("the abandoned head itself was published", func(t *testing.T) {
		t.Parallel()
		f := newAbandonedFixture(t)
		liveHead := f.liveOn(t, f.published)
		_, err := ReconcileStaleBranch(ctx, f.gateDir, f.work, "feature", liveHead, AbandonedSubmission{Head: f.abandonedAt, Publications: []string{f.abandonedAt}})
		if err == nil || !strings.Contains(err.Error(), f.abandonedAt) {
			t.Fatalf("published head replaced under the abandoned-submission exception: err=%v", err)
		}
		f.assertUntouched(t)
	})

	t.Run("a publication the gate cannot resolve", func(t *testing.T) {
		t.Parallel()
		f := newAbandonedFixture(t)
		liveHead := f.liveOn(t, f.published)
		unknown := strings.Repeat("a", len(f.abandonedAt))
		if _, err := ReconcileStaleBranch(ctx, f.gateDir, f.work, "feature", liveHead, AbandonedSubmission{Head: f.abandonedAt, Publications: []string{unknown}}); err == nil {
			t.Fatal("an unresolvable publication was treated as proof that nothing was published")
		}
		f.assertUntouched(t)
	})

	t.Run("a published ancestor the live head keeps", func(t *testing.T) {
		t.Parallel()
		f := newAbandonedFixture(t)
		liveHead := f.liveOn(t, f.published)
		result, err := ReconcileStaleBranch(ctx, f.gateDir, f.work, "feature", liveHead, AbandonedSubmission{Head: f.abandonedAt, Publications: []string{f.published}})
		if err != nil {
			t.Fatalf("only the never-published commit is dropped, yet it was refused: %v", err)
		}
		if !result.Reconciled || result.PreviousHead != f.abandonedAt {
			t.Fatalf("reconciliation result = %+v", result)
		}
	})

	t.Run("a head other than the abandoned submission", func(t *testing.T) {
		t.Parallel()
		f := newAbandonedFixture(t)
		liveHead := f.liveOn(t, f.published)
		if _, err := ReconcileStaleBranch(ctx, f.gateDir, f.work, "feature", liveHead, AbandonedSubmission{Head: f.published}); err == nil {
			t.Fatal("a private head the tool did not record as the abandoned submission was replaced")
		}
		f.assertUntouched(t)
		abbreviated := f.abandonedAt[:12]
		if _, err := ReconcileStaleBranch(ctx, f.gateDir, f.work, "feature", liveHead, AbandonedSubmission{Head: abbreviated}); err == nil {
			t.Fatal("an abbreviated abandoned head was accepted as exact")
		}
		f.assertUntouched(t)
	})
}

func TestPlanStaleBranchReconciliationAbandonedSubmissionMutatesNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newAbandonedFixture(t)
	liveHead := f.liveOn(t, f.published)
	plan, err := PlanStaleBranchReconciliation(ctx, f.gateDir, f.work, "feature", liveHead, AbandonedSubmission{Head: f.abandonedAt, Publications: []string{f.published}})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Reconcile || plan.PreviousHead != f.abandonedAt {
		t.Fatalf("plan = %+v", plan)
	}
	f.assertUntouched(t)
}
