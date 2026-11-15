package steps

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

const (
	prePushLintBadFile = "lint-bad.txt"
	prePushLintMessage = "lint: lint-bad.txt is forbidden"
	prePushTestMarker  = "test-ran.txt"
)

// usePrePushLintFixture configures a lint command that fails exactly while
// lint-bad.txt exists, and a test command that leaves a marker if it ever runs.
func usePrePushLintFixture(f *ciRepairFixture) {
	f.sctx.Config.Commands.Lint = "if [ -f " + prePushLintBadFile + " ]; then echo '" + prePushLintMessage + "'; exit 1; fi"
	f.sctx.Config.Commands.Test = "touch " + prePushTestMarker
}

func writeLintBadFix(workDir string) {
	writeCIFix(workDir)
	os.WriteFile(filepath.Join(workDir, prePushLintBadFile), []byte("bad"), 0o644)
}

func ciFixCalls(ag *mockAgent) []agent.RunOpts {
	var calls []agent.RunOpts
	for _, call := range ag.calls {
		if strings.Contains(call.Prompt, "CI") {
			calls = append(calls, call)
		}
	}
	return calls
}

func assertTestCommandNeverRan(t *testing.T, f *ciRepairFixture) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(f.dir, prePushTestMarker)); err == nil {
		t.Error("the pre-push checks ran commands.test")
	}
}

func TestCIStep_RepairFailingLintParksInsteadOfPushing(t *testing.T) {
	f := newCIRepairFixture(t, false, nil)
	usePrePushLintFixture(f)
	ag := &mockAgent{name: "test", runFn: func(ctx context.Context, opts agent.RunOpts) (*agent.Result, error) {
		writeLintBadFix(opts.CWD)
		return &agent.Result{Output: []byte(`{"summary":"repair the failing check"}`)}, nil
	}}
	f.sctx.Agent = ag

	outcome, err := f.run(t)
	if err != nil {
		t.Fatalf("CI step returned error: %v\nlog:\n%s", err, f.log())
	}
	if got := f.remoteHead(t); got != f.headSHA {
		t.Fatalf("remote head = %s, want the unrepaired head %s: a repair failing lint was pushed", got, f.headSHA)
	}
	if outcome == nil || !outcome.NeedsApproval {
		t.Fatalf("outcome = %#v, want the CI step parked", outcome)
	}
	findings, err := types.ParseFindingsJSON(outcome.Findings)
	if err != nil {
		t.Fatal(err)
	}
	var parked bool
	for _, item := range findings.Items {
		if item.ID == ciPrePushCheckFindingID && item.Action == types.ActionAskUser {
			parked = true
		}
	}
	if !parked {
		t.Errorf("findings = %+v, want the %s ask-user finding", findings.Items, ciPrePushCheckFindingID)
	}
	if !strings.Contains(findings.Summary, prePushLintMessage) {
		t.Errorf("park summary = %q, want the lint output", findings.Summary)
	}
	calls := ciFixCalls(ag)
	if len(calls) != 1+ciPrePushFixTurns {
		t.Fatalf("CI fixer turns = %d, want %d", len(calls), 1+ciPrePushFixTurns)
	}
	for _, call := range calls[1:] {
		if !strings.Contains(call.Prompt, prePushLintMessage) {
			t.Errorf("a follow-up fixer turn did not receive the lint output:\n%s", call.Prompt)
		}
	}
	run, err := f.sctx.DB.GetRun(f.sctx.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.LastPushedSHA != nil && *run.LastPushedSHA != f.headSHA {
		t.Errorf("push binding = %s, want the unrepaired head %s", *run.LastPushedSHA, f.headSHA)
	}
	assertTestCommandNeverRan(t, f)
}

func TestCIStep_RepairIsPushedOnlyAfterTheFixerClearsTheLintFailure(t *testing.T) {
	f := newCIRepairFixture(t, false, nil)
	usePrePushLintFixture(f)
	ag := &mockAgent{name: "test"}
	ag.runFn = func(ctx context.Context, opts agent.RunOpts) (*agent.Result, error) {
		if strings.Contains(opts.Prompt, prePushLintMessage) {
			os.Remove(filepath.Join(opts.CWD, prePushLintBadFile))
		} else {
			writeLintBadFix(opts.CWD)
		}
		return &agent.Result{Output: []byte(`{"summary":"repair the failing check"}`)}, nil
	}
	f.sctx.Agent = ag

	// A published repair keeps the monitor polling until the fixture's poll
	// budget cancels it, so cancellation is the success path here.
	if _, err := f.run(t); !errors.Is(err, context.Canceled) {
		t.Fatalf("CI step error = %v, want the monitor still polling after the published repair\nlog:\n%s", err, f.log())
	}
	if len(ciFixCalls(ag)) != 2 {
		t.Fatalf("CI fixer turns = %d, want the repair turn and one lint-fix turn", len(ciFixCalls(ag)))
	}
	local := f.localHead(t)
	if local == f.headSHA {
		t.Fatal("the repair commit was never created")
	}
	if got := f.remoteHead(t); got != local {
		t.Fatalf("remote head = %s, want the lint-clean repair %s", got, local)
	}
	if out := gitCmd(t, f.dir, "ls-tree", "--name-only", "HEAD"); strings.Contains(out, prePushLintBadFile) {
		t.Errorf("the pushed repair still carries %s", prePushLintBadFile)
	}
	assertTestCommandNeverRan(t, f)
}

func TestCIStep_CommitRepairWithoutAFixerTurnRefusesToPushAHeadFailingLint(t *testing.T) {
	f := newCIRepairFixture(t, false, nil)
	usePrePushLintFixture(f)
	writeLintBadFix(f.dir)

	_, err := (&CIStep{}).commitRepair(f.sctx, "repair the failing check", nil)
	var failure *prePushCheckFailure
	if !errors.As(err, &failure) {
		t.Fatalf("commitRepair() error = %v, want a pre-push check failure", err)
	}
	if !strings.Contains(failure.Output, prePushLintMessage) {
		t.Errorf("failure output = %q, want the lint output", failure.Output)
	}
	if got := f.remoteHead(t); got != f.headSHA {
		t.Fatalf("remote head = %s, want the unrepaired head %s", got, f.headSHA)
	}
	assertTestCommandNeverRan(t, f)
}

func TestCIStep_RevalidatingRepairLeavesLintToTheLintStep(t *testing.T) {
	f := newCIRepairFixture(t, true, nil)
	usePrePushLintFixture(f)
	writeLintBadFix(f.dir)

	repair, err := (&CIStep{}).commitRepair(f.sctx, "repair the failing check", nil)
	if err != nil {
		t.Fatalf("commitRepair() error = %v, want the repair held for revalidation", err)
	}
	if !repair.Revalidate {
		t.Fatal("repair was not held for revalidation")
	}
	if got := f.remoteHead(t); got != f.headSHA {
		t.Fatalf("remote head = %s, want the unrepaired head %s", got, f.headSHA)
	}
}
