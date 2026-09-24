package steps

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
)

type ciRebaseRepublication struct {
	sctx          *pipeline.StepContext
	upstream      string
	gateDir       string
	publishedHead string
	repairedHead  string
}

// afterPublish runs after the first publication and before main moves.
func publishThenRebaseOverMovedMain(t *testing.T, afterPublish func(f *ciRebaseRepublication, dir string)) *ciRebaseRepublication {
	t.Helper()
	upstream := t.TempDir()
	gitCmd(t, upstream, "init", "--bare")
	dir, baseSHA, submittedHead := setupGitRepo(t)
	gitCmd(t, dir, "remote", "add", "origin", upstream)
	gitCmd(t, dir, "push", "origin", "main")

	sctx := newTestContextWithDBRecords(t, &mockAgent{name: "test"}, dir, baseSHA, submittedHead, config.Commands{})
	sctx.Repo.UpstreamURL = upstream
	sctx.Run.Branch = "refs/heads/feature"
	gateDir := setupGateMirror(t, sctx)
	gitCmd(t, gateDir, "fetch", dir, submittedHead+":refs/heads/feature")

	if err := os.WriteFile(filepath.Join(dir, "feature.txt"), []byte("feature code\nreviewed fix\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "commit", "-am", "no-mistakes(review): tighten the feature")
	publishedHead := gitCmd(t, dir, "rev-parse", "HEAD")
	sctx.Run.HeadSHA = publishedHead
	recordReviewApproval(t, sctx, publishedHead)
	if _, err := (&PushStep{}).Execute(sctx); err != nil {
		t.Fatalf("first publication failed: %v", err)
	}
	if got := gitCmd(t, gateDir, "rev-parse", "refs/heads/feature"); got != publishedHead {
		t.Fatalf("gate mirror after first publication = %s, want %s", got, publishedHead)
	}
	f := &ciRebaseRepublication{sctx: sctx, upstream: upstream, gateDir: gateDir, publishedHead: publishedHead}
	if afterPublish != nil {
		afterPublish(f, dir)
	}

	other := t.TempDir()
	gitCmd(t, other, "clone", upstream, ".")
	gitCmd(t, other, "checkout", "main")
	if err := os.WriteFile(filepath.Join(other, "feature.txt"), []byte("main's own feature.txt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, other, "add", "-A")
	gitCmd(t, other, "commit", "-m", "another PR lands the same file on main")
	gitCmd(t, other, "push", "origin", "main")

	gitCmd(t, dir, "fetch", "origin", "main")
	if _, err := stepGitRun(sctx, "rebase", "origin/main"); err == nil {
		t.Fatal("expected the rebase onto the moved main to conflict")
	}
	if err := os.WriteFile(filepath.Join(dir, "feature.txt"), []byte("main's own feature.txt\nfeature code\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "add", "-A")
	if _, err := stepGitRun(sctx, "-c", "core.editor=true", "rebase", "--continue"); err != nil {
		t.Fatalf("resolve the conflict: %v", err)
	}
	repair, err := (&CIStep{}).commitRepair(sctx, "resolve merge conflict")
	if err != nil {
		t.Fatalf("CI repair failed: %v", err)
	}
	if !repair.Revalidate {
		t.Fatalf("CI conflict repair = %#v, want it held for revalidation", repair)
	}
	f.repairedHead = gitCmd(t, dir, "rev-parse", "HEAD")
	recordReviewApproval(t, sctx, f.repairedHead)
	return f
}

func TestPushStep_PublishesCIRebaseOverItsOwnEarlierPublication(t *testing.T) {
	f := publishThenRebaseOverMovedMain(t, nil)

	if _, err := (&PushStep{}).Execute(f.sctx); err != nil {
		t.Fatalf("push refused the run's own rebased republication: %v", err)
	}
	if got := gitCmd(t, f.upstream, "rev-parse", "refs/heads/feature"); got != f.repairedHead {
		t.Fatalf("remote head = %s, want %s", got, f.repairedHead)
	}
	if got := gitCmd(t, f.gateDir, "rev-parse", "refs/heads/feature"); got != f.repairedHead {
		t.Fatalf("gate mirror ref = %s, want %s", got, f.repairedHead)
	}
	archived := gitCmd(t, f.gateDir, "rev-parse", "refs/tags/no-mistakes-abandoned/feature/"+f.publishedHead+"^{commit}")
	if archived != f.publishedHead {
		t.Fatalf("archived earlier publication = %s, want %s", archived, f.publishedHead)
	}
}

func TestPushStep_CIRebaseStillRefusesPrivateCommitOnTopOfItsOwnPublication(t *testing.T) {
	var privateHead string
	f := publishThenRebaseOverMovedMain(t, func(f *ciRebaseRepublication, dir string) {
		if err := os.WriteFile(filepath.Join(dir, "private.txt"), []byte("work only the gate carries\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitCmd(t, dir, "add", "private.txt")
		gitCmd(t, dir, "commit", "-m", "private gate work")
		privateHead = gitCmd(t, dir, "rev-parse", "HEAD")
		gitCmd(t, f.gateDir, "fetch", dir, privateHead+":refs/heads/feature")
		gitCmd(t, dir, "reset", "--hard", f.publishedHead)
	})

	_, err := (&PushStep{}).Execute(f.sctx)
	if err == nil || !strings.Contains(err.Error(), privateHead) || !strings.Contains(err.Error(), "private gate work") {
		t.Fatalf("push did not refuse over the private commit %s: %v", privateHead, err)
	}
	if got := gitCmd(t, f.upstream, "rev-parse", "refs/heads/feature"); got != f.publishedHead {
		t.Fatalf("remote moved to %s, want the earlier publication %s", got, f.publishedHead)
	}
	if got := gitCmd(t, f.gateDir, "rev-parse", "refs/heads/feature"); got != privateHead {
		t.Fatalf("gate mirror ref = %s, want the private head %s", got, privateHead)
	}
	if tags := gitCmd(t, f.gateDir, "tag", "--list", "no-mistakes-abandoned/*"); tags != "" {
		t.Fatalf("refused push archived the gate branch: %q", tags)
	}
}
