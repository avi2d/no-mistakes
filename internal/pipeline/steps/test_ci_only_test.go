package steps

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/reviewqa"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func TestTestStep_CIOnlyDoesNotRunPreparationBaselineOrRepair(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)
	calls := 0
	ag := &mockAgent{
		name: "preview-agent",
		runFn: func(_ context.Context, opts agent.RunOpts) (*agent.Result, error) {
			calls++
			if !opts.NoTools || !strings.Contains(opts.Prompt, "Do not run tests") ||
				!strings.Contains(opts.Prompt, "product.png") || strings.Contains(opts.Prompt, "exit 123") {
				t.Fatalf("not an evidence-only turn: no tools=%t, prompt=%q", opts.NoTools, opts.Prompt)
			}
			return &agent.Result{Output: json.RawMessage(`{"preferred":2}`)}, nil
		},
	}
	sctx := newTestContext(t, ag, dir, baseSHA, headSHA, config.Commands{Prepare: "exit 123", Test: "exit 123"})
	sctx.Config.Test.CIOnly = true
	sctx.Config.Test.Prepare = true
	sctx.Config.Test.BaseAttribution = true
	sctx.Config.CommandOverrides = map[string]config.CommandOverride{"test": {Additional: []string{"exit 123"}}}
	sctx.Fixing = true

	var logged []string
	sctx.Log = func(line string) { logged = append(logged, line) }
	for _, name := range []string{"alternative.png", "product.png", filepath.Join(reviewqa.DirName, "private.png")} {
		path := filepath.Join(sctx.EvidenceDir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	out, err := (&TestStep{}).Execute(sctx)
	if err != nil {
		t.Fatal(err)
	}
	findings, err := types.ParseFindingsJSON(out.Findings)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || out.NeedsApproval || out.ExitCode != 0 || out.FixSummary != "" ||
		findings.Verdict != "" || findings.TestedHeadSHA != "" || len(findings.Tested) != 0 {
		t.Fatalf("calls=%d, outcome=%+v, findings=%+v", calls, out, findings)
	}
	if len(findings.Artifacts) != 2 || findings.Artifacts[0].Path != filepath.Join(sctx.EvidenceDir, "product.png") ||
		findings.Artifacts[1].Path != filepath.Join(sctx.EvidenceDir, "alternative.png") {
		t.Fatalf("unexpected public evidence: %+v", findings.Artifacts)
	}
	const deferral = "Repository tests are left to CI."
	testingSummary := BuildTestingSummary([]*db.StepResult{
		{StepName: types.StepTest, Status: types.StepStatusCompleted, FindingsJSON: &out.Findings},
	}, nil)
	if !strings.Contains(strings.Join(logged, "\n"), deferral) || !strings.Contains(testingSummary, deferral) {
		t.Fatalf("CI deferral missing from log %q or PR summary %q", logged, testingSummary)
	}
}

func TestTestStep_CIOnlyWithoutMediaDoesNotLaunchAnAgent(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)
	ag := &mockAgent{
		name: "unexpected",
		runFn: func(context.Context, agent.RunOpts) (*agent.Result, error) {
			t.Fatal("no media was available for the agent")
			return nil, nil
		},
	}
	sctx := newTestContext(t, ag, dir, baseSHA, headSHA, config.Commands{})
	sctx.Config.Test.CIOnly = true

	out, err := (&TestStep{}).Execute(sctx)
	if err != nil {
		t.Fatal(err)
	}
	if out.NeedsApproval || !strings.Contains(out.Findings, "No preview evidence was available.") {
		t.Fatalf("unexpected evidence outcome: %+v", out)
	}
}
