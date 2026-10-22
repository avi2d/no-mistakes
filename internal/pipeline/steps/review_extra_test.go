package steps

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

const extraReviewCleanJSON = `{"findings":[],"summary":"clean","reviewed_paths":["feature.txt"],"risk_level":"low","risk_rationale":"no untrusted input","risk_scope":"source-or-external"}`

func extraReviewFindingJSON() string {
	return `{"findings":[{"severity":"error","description":"notes on style","action":"no-op"}],"summary":"extra","reviewed_paths":["feature.txt"],"risk_level":"low","risk_rationale":"no untrusted input","risk_scope":"source-or-external"}`
}

func outcomeExtraFindings(t *testing.T, outcomeJSON string) []types.Finding {
	t.Helper()
	parsed, err := types.ParseFindingsJSON(outcomeJSON)
	if err != nil {
		t.Fatalf("parse outcome findings: %v", err)
	}
	return parsed.Items
}

func extraReviewTestAgent(freeResult *agent.Result, freeErr error) (*mockFreeModelAgent, *int) {
	primaryCalls := 0
	ag := newMockFreeModelAgent(func(context.Context, agent.RunOpts) (*agent.Result, error) {
		primaryCalls++
		return &agent.Result{Output: json.RawMessage(extraReviewCleanJSON)}, nil
	})
	ag.freeFn = func(context.Context, agent.FreeModelKind, agent.RunOpts) (*agent.Result, error) {
		return freeResult, freeErr
	}
	return ag, &primaryCalls
}

func TestExtraReview_DisabledRunsNormalReviewOnly(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)
	ag, primaryCalls := extraReviewTestAgent(&agent.Result{Output: json.RawMessage(extraReviewFindingJSON())}, nil)
	sctx := freeModelStepContext(t, ag, dir, baseSHA, headSHA)
	sctx.Config.MinimaxExtraReview.Enabled = false
	outcome, err := (&ReviewStep{}).Execute(sctx)
	if err != nil {
		t.Fatal(err)
	}
	if ag.freeCalls != 0 {
		t.Fatal("extra reviewer ran while disabled")
	}
	if *primaryCalls != 1 {
		t.Fatalf("primary calls = %d, want 1", *primaryCalls)
	}
	if findings := outcomeExtraFindings(t, outcome.Findings); len(findings) != 0 {
		t.Fatalf("findings = %d, want none", len(findings))
	}
}

func TestExtraReview_AdvisoryNeverBlocks(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)
	ag, _ := extraReviewTestAgent(&agent.Result{Output: json.RawMessage(extraReviewFindingJSON())}, nil)
	sctx := freeModelStepContext(t, ag, dir, baseSHA, headSHA)
	outcome, err := (&ReviewStep{}).Execute(sctx)
	if err != nil {
		t.Fatal(err)
	}
	if ag.freeCalls != 1 {
		t.Fatalf("extra calls = %d, want 1", ag.freeCalls)
	}
	if outcome.NeedsApproval {
		t.Fatal("advisory extra findings parked the review")
	}
	if outcome.AutoFixable {
		t.Fatal("advisory extra findings opened a fix round")
	}
	findings := outcomeExtraFindings(t, outcome.Findings)
	if len(findings) != 1 {
		t.Fatalf("findings = %d, want the single advisory note", len(findings))
	}
	got := findings[0]
	if got.Severity != types.FindingSeverityInfo || got.Action != types.ActionNoOp || got.Category != types.FindingCategoryExtraReview {
		t.Fatalf("advisory finding not stamped info/no-op/extra-review: %+v", got)
	}
	if !strings.HasPrefix(got.Description, "Advisory: ") {
		t.Fatalf("advisory description lost its prefix: %q", got.Description)
	}
}

func TestExtraReview_FailureFallsBackThenStandsAlone(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)
	ag, _ := extraReviewTestAgent(nil, context.DeadlineExceeded)
	ag.fallbackFn = func(context.Context, agent.FreeModelKind, agent.RunOpts) (*agent.Result, error) {
		return &agent.Result{Output: json.RawMessage(extraReviewFindingJSON())}, nil
	}
	sctx := freeModelStepContext(t, ag, dir, baseSHA, headSHA)
	outcome, err := (&ReviewStep{}).Execute(sctx)
	if err != nil {
		t.Fatal(err)
	}
	if ag.fallbackCalls != 1 {
		t.Fatalf("fallback calls = %d, want 1", ag.fallbackCalls)
	}
	if findings := outcomeExtraFindings(t, outcome.Findings); len(findings) != 1 || outcome.NeedsApproval {
		t.Fatalf("outcome = %+v, want one advisory finding without a park", outcome)
	}
}

func TestExtraReview_TotalFailureKeepsNormalReview(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)
	ag, _ := extraReviewTestAgent(nil, context.DeadlineExceeded)
	ag.fallbackFn = func(context.Context, agent.FreeModelKind, agent.RunOpts) (*agent.Result, error) {
		return nil, context.DeadlineExceeded
	}
	sctx := freeModelStepContext(t, ag, dir, baseSHA, headSHA)
	outcome, err := (&ReviewStep{}).Execute(sctx)
	if err != nil {
		t.Fatal(err)
	}
	if findings := outcomeExtraFindings(t, outcome.Findings); len(findings) != 0 || outcome.NeedsApproval {
		t.Fatalf("outcome = %+v, want the clean normal review standing alone", outcome)
	}
}

func TestExtraReview_WorktreeMutationDiscardsFindings(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)
	ag, _ := extraReviewTestAgent(&agent.Result{Output: json.RawMessage(extraReviewFindingJSON())}, nil)
	ag.freeFn = func(_ context.Context, _ agent.FreeModelKind, opts agent.RunOpts) (*agent.Result, error) {
		if err := os.WriteFile(filepath.Join(opts.CWD, "feature.txt"), []byte("mutated"), 0o644); err != nil {
			return nil, err
		}
		return &agent.Result{Output: json.RawMessage(extraReviewFindingJSON())}, nil
	}
	sctx := freeModelStepContext(t, ag, dir, baseSHA, headSHA)
	outcome, err := (&ReviewStep{}).Execute(sctx)
	if err != nil {
		t.Fatal(err)
	}
	if findings := outcomeExtraFindings(t, outcome.Findings); len(findings) != 0 {
		t.Fatalf("findings = %d, want none after a mutating extra turn", len(findings))
	}
	content, err := os.ReadFile(filepath.Join(dir, "feature.txt"))
	if err != nil || string(content) != "feature code"+"\n" {
		t.Fatalf("feature.txt = %q after restore", content)
	}
}

func TestExtraReview_EvalReplaySkips(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)
	ag, _ := extraReviewTestAgent(&agent.Result{Output: json.RawMessage(extraReviewFindingJSON())}, nil)
	sctx := freeModelStepContext(t, ag, dir, baseSHA, headSHA)
	sctx.EvalReplay = true
	outcome, err := (&ReviewStep{}).Execute(sctx)
	if err != nil {
		t.Fatal(err)
	}
	if ag.freeCalls != 0 {
		t.Fatal("extra reviewer ran during eval replay")
	}
	if findings := outcomeExtraFindings(t, outcome.Findings); len(findings) != 0 {
		t.Fatalf("findings = %d, want none", len(findings))
	}
}

func TestStampExtraReviewFindings_ForcesAdvisory(t *testing.T) {
	t.Parallel()
	findings := []types.Finding{
		{ID: "r1", Severity: types.FindingSeverityError, Action: types.ActionAutoFix, Description: "fix this"},
		{ID: "r2", Severity: types.FindingSeverityWarning, Action: types.ActionAskUser, Description: "Advisory: already"},
	}
	stamped := stampExtraReviewFindings(findings)
	if len(stamped) != 2 {
		t.Fatalf("stamped = %d, want 2", len(stamped))
	}
	for _, got := range stamped {
		if got.Severity != types.FindingSeverityInfo || got.Action != types.ActionNoOp || got.Category != types.FindingCategoryExtraReview {
			t.Fatalf("finding not forced advisory: %+v", got)
		}
		if !strings.HasPrefix(got.Description, "Advisory: ") {
			t.Fatalf("description lost its prefix: %q", got.Description)
		}
	}
	if strings.Count(stamped[1].Description, "Advisory: ") != 1 {
		t.Fatalf("prefix duplicated: %q", stamped[1].Description)
	}
}

func TestExtraReview_UnlistedRepoSkips(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)
	ag, _ := extraReviewTestAgent(&agent.Result{Output: json.RawMessage(extraReviewFindingJSON())}, nil)
	sctx := freeModelStepContext(t, ag, dir, baseSHA, headSHA)
	sctx.Repo.UpstreamURL = "https://github.com/o/beta"
	outcome, err := (&ReviewStep{}).Execute(sctx)
	if err != nil {
		t.Fatal(err)
	}
	if ag.freeCalls != 0 {
		t.Fatal("extra reviewer ran for an unlisted repo")
	}
	if findings := outcomeExtraFindings(t, outcome.Findings); len(findings) != 0 {
		t.Fatalf("findings = %d, want none", len(findings))
	}
}
