package steps

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/paths"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

// The housekeeping agent edits the tree on every pass, the way a model finds
// one more doc to touch each time it rereads the same change. After Push asks
// Review to revalidate the recorded decision and Review approves that exact
// tree, the run must publish it instead of letting a second housekeeping pass
// rewrite it.
func TestRecordedDecisionRevalidation_RepeatedHousekeepingDoesNotBlockPublication(t *testing.T) {
	for _, tc := range []struct {
		name    string
		lintCmd string
	}{
		{name: "combined_document_lint_pass"},
		{name: "configured_lint_command", lintCmd: "true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			upstream := t.TempDir()
			gitCmd(t, upstream, "init", "--bare")
			dir, baseSHA, headSHA := setupGitRepo(t)
			gitCmd(t, dir, "remote", "add", "origin", upstream)
			gitCmd(t, dir, "push", "origin", "main")

			database, err := db.Open(filepath.Join(t.TempDir(), "state.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { database.Close() })
			repo, err := database.InsertRepo(dir, upstream, "main")
			if err != nil {
				t.Fatal(err)
			}
			run, err := database.InsertRun(repo.ID, "refs/heads/feature", headSHA, baseSHA)
			if err != nil {
				t.Fatal(err)
			}

			reviews, housekeepingPasses, lintPasses := 0, 0, 0
			editEveryPass := func(file string, pass int) {
				path := filepath.Join(dir, file)
				existing, _ := os.ReadFile(path)
				content := append(existing, []byte(fmt.Sprintf("pass %d touched one more line\n", pass))...)
				if err := os.WriteFile(path, content, 0o644); err != nil {
					t.Error(err)
				}
			}
			ag := &mockAgent{name: "test", runFn: func(_ context.Context, opts agent.RunOpts) (*agent.Result, error) {
				switch opts.Purpose {
				case "review":
					reviews++
					if reviews == 1 {
						return &agent.Result{Output: json.RawMessage(`{"findings":[{"id":"prefix","severity":"warning","file":"feature.txt","description":"choose the identifier prefix","action":"ask-user"}],"summary":"one decision","risk_level":"low","risk_rationale":"bounded","risk_scope":"source-or-external"}`)}, nil
					}
					findings := cleanReviewFindings()
					findings.ReviewedPaths = fullReviewCoverage(t, dir, baseSHA)
					findings.DecisionReviews = satisfiedDecisionReviews(t, opts.Prompt)
					output, err := json.Marshal(findings)
					return &agent.Result{Output: output}, err
				case "review-fix":
					if err := os.WriteFile(filepath.Join(dir, "feature.txt"), []byte("decided prefix\n"), 0o644); err != nil {
						t.Error(err)
					}
					return &agent.Result{Output: json.RawMessage(`{"summary":"apply the decided prefix"}`)}, nil
				case "document", "housekeeping":
					housekeepingPasses++
					editEveryPass("NOTES.md", housekeepingPasses)
					return &agent.Result{Output: json.RawMessage(fmt.Sprintf(`{"findings":[],"summary":"note pass %d"}`, housekeepingPasses))}, nil
				case "lint":
					lintPasses++
					editEveryPass("LINT.md", lintPasses)
					return &agent.Result{Output: json.RawMessage(fmt.Sprintf(`{"findings":[],"summary":"lint pass %d"}`, lintPasses))}, nil
				}
				t.Errorf("unexpected agent purpose %q", opts.Purpose)
				return &agent.Result{Output: json.RawMessage(`{}`)}, nil
			}}

			cfg := &config.Config{
				Agent:    types.AgentClaude,
				AutoFix:  config.AutoFix{Review: 3},
				Commands: config.Commands{Lint: tc.lintCmd},
			}
			steps := []pipeline.Step{&ReviewStep{}, &DocumentStep{}, &LintStep{}, &PushStep{}}
			executor := pipeline.NewExecutor(database, paths.WithRoot(t.TempDir()), cfg, ag, steps, nil)
			done := make(chan error, 1)
			go func() { done <- executor.Execute(context.Background(), run, repo, dir) }()

			waitForReviewStatus(t, database, run.ID, types.StepStatusAwaitingApproval)
			if err := executor.Respond(types.StepReview, types.ActionFix, []string{"prefix"}); err != nil {
				t.Fatal(err)
			}
			select {
			case err = <-done:
			case <-time.After(60 * time.Second):
				t.Fatal("pipeline did not finish")
			}
			if err != nil {
				t.Fatalf("run failed after recorded-decision revalidation: %v", err)
			}

			final, err := database.GetRun(run.ID)
			if err != nil {
				t.Fatal(err)
			}
			local := gitCmd(t, dir, "rev-parse", "HEAD")
			remote := gitCmd(t, upstream, "rev-parse", "refs/heads/feature")
			if final.ReviewApprovedHeadSHA == nil || *final.ReviewApprovedHeadSHA != remote || remote != local {
				t.Fatalf("published head is not the review-approved head: approved=%v local=%s remote=%s", final.ReviewApprovedHeadSHA, local, remote)
			}
			if housekeepingPasses != 1 || lintPasses != 0 || reviews != 3 {
				t.Fatalf("housekeeping repeated over its own settled tree: reviews=%d housekeeping=%d lint=%d", reviews, housekeepingPasses, lintPasses)
			}
			if !strings.Contains(gitCmd(t, upstream, "show", "refs/heads/feature:NOTES.md"), "pass 1") {
				t.Fatal("published head lost the first housekeeping pass")
			}
			t.Logf("approved=%s remote=%s reviews=%d housekeeping=%d lint=%d", *final.ReviewApprovedHeadSHA, remote, reviews, housekeepingPasses, lintPasses)
		})
	}
}
