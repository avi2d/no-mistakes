package steps

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/ipc"
	"github.com/kunchenguid/no-mistakes/internal/paths"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func TestReviewLaterFixDoesNotSendWarningsOrInfoToTheFixer(t *testing.T) {
	for _, tc := range []struct {
		name       string
		mixed      bool
		fixCount   int
		roundCount int
	}{
		{"warning and info only", false, 1, 2},
		{"error and follow-ups", true, 2, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir, base, _ := setupGitRepo(t)
			for _, file := range []string{"settings.txt", "second.txt"} {
				if err := os.WriteFile(filepath.Join(dir, file), []byte("original\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			gitCmd(t, dir, "add", "settings.txt", "second.txt")
			gitCmd(t, dir, "commit", "-m", "extend review scope")
			head := strings.TrimSpace(gitCmd(t, dir, "rev-parse", "HEAD"))
			gitCmd(t, dir, "checkout", "--detach", head)

			first := Finding{Severity: "warning", Action: types.ActionAutoFix, File: "feature.txt", Description: "fix this first-gate warning"}
			warn := Finding{Severity: "warning", Action: types.ActionAutoFix, File: "settings.txt", Description: "use one name for the setting"}
			blocking := Finding{Severity: "error", Action: types.ActionAutoFix, File: "second.txt", Description: "reject a missing value"}
			info := types.Finding{Severity: "info", Action: types.ActionNoOp, Description: "document the new setting"}
			reviews, fixes := 0, 0
			ag := &mockAgent{name: "review-follow-ups", runFn: func(_ context.Context, opts agent.RunOpts) (*agent.Result, error) {
				if opts.Purpose == "review-fix" {
					fixes++
					file := "feature.txt"
					if fixes > 1 {
						if !tc.mixed || !strings.Contains(opts.Prompt, blocking.Description) {
							return nil, fmt.Errorf("unexpected follow-up fixer")
						}
						_, selection, ok := strings.Cut(opts.Prompt, "Previous review findings to address:")
						if !ok {
							return nil, fmt.Errorf("fixer did not receive a finding selection")
						}
						if strings.Contains(selection, warn.Description) || strings.Contains(selection, info.Description) {
							return nil, fmt.Errorf("fixer received non-errors: %s", selection)
						}
						file = "second.txt"
					}
					if err := os.WriteFile(filepath.Join(opts.CWD, file), []byte("corrected\n"), 0o644); err != nil {
						return nil, err
					}
					return &agent.Result{Output: []byte(`{"summary":"apply selected correction"}`)}, nil
				}
				reviews++
				out := cleanReviewFindings()
				out.ReviewedPaths = []string{"feature.txt", "settings.txt", "second.txt"}
				switch reviews {
				case 1:
					out.Items = []Finding{first}
				case 2:
					out.Items = []Finding{warn}
					if tc.mixed {
						out.Items = append(out.Items, blocking)
					}
				default:
					out.Items = []Finding{warn}
				}
				raw, err := json.Marshal(out)
				return &agent.Result{Output: raw}, err
			}}
			sctx := newTestContextWithDBRecords(t, ag, dir, base, head, config.Commands{})
			sctx.Config.Review.DeferNonErrors = true
			executor, parks, done := startFollowUpReview(t, sctx, ag)
			gate := waitFollowUpReviewPark(t, parks, done)
			if err := executor.Respond(types.StepReview, types.ActionFix, []string{gate.Items[0].ID}); err != nil {
				t.Fatal(err)
			}
			gate = waitFollowUpReviewPark(t, parks, done)
			var ids []string
			var warningID, blockingID string
			for _, finding := range gate.Items {
				ids = append(ids, finding.ID)
				switch finding.Description {
				case warn.Description:
					warningID = finding.ID
				case blocking.Description:
					blockingID = finding.ID
				}
			}
			if warningID == "" || (tc.mixed && blockingID == "") {
				t.Fatalf("second gate did not present the fixture findings: %+v", gate.Items)
			}
			dispositions, err := executor.RespondWithOverrides(types.StepReview, types.ActionFix, ids, nil, nil, []types.Finding{info}, "")
			if err != nil {
				t.Fatal(err)
			}
			if len(dispositions.FollowUps) != 2 || dispositions.FollowUps[0] != warningID {
				t.Fatalf("response did not echo the two follow-ups: %+v", dispositions)
			}
			var wantFixed []string
			if tc.mixed {
				wantFixed = []string{blockingID}
			}
			if !slices.Equal(dispositions.Fixed, wantFixed) {
				t.Fatalf("fixed=%v, want %v", dispositions.Fixed, wantFixed)
			}
			waitFollowUpReviewDone(t, parks, done)
			results, err := sctx.DB.GetStepsByRun(sctx.Run.ID)
			if err != nil {
				t.Fatal(err)
			}
			rounds, err := sctx.DB.GetRoundsByStep(results[0].ID)
			if err != nil {
				t.Fatal(err)
			}
			if fixes != tc.fixCount || reviews != tc.roundCount || len(rounds) != tc.roundCount {
				t.Fatalf("fixers=%d, reviewers=%d, stored rounds=%d; want %d, %d, %d", fixes, reviews, len(rounds), tc.fixCount, tc.roundCount, tc.roundCount)
			}
			body, _ := BuildPipelineSummary(results, map[string][]*db.StepRound{results[0].ID: rounds}, sctx.Run.HeadSHA)
			_, followUps, ok := strings.Cut(body, "### Review follow-ups\n")
			if !ok || !strings.Contains(followUps, warn.Description) || !strings.Contains(followUps, info.Description) || strings.Contains(followUps, blocking.Description) || strings.Contains(followUps, first.Description) {
				t.Fatalf("wrong PR follow-ups:\n%s", body)
			}
		})
	}
}

func TestReviewUnchangedFixDoesNotRereviewOrVerifyAnOutstandingError(t *testing.T) {
	t.Parallel()
	dir, base, head := setupGitRepo(t)
	gitCmd(t, dir, "checkout", "--detach", head)
	const description = "reject an empty name"
	reviews, fixes := 0, 0
	ag := &mockAgent{name: "unchanged-review-fix", runFn: func(_ context.Context, opts agent.RunOpts) (*agent.Result, error) {
		if opts.Purpose == "review-fix" {
			fixes++
			return &agent.Result{Output: []byte(`{"summary":"no edits"}`)}, nil
		}
		reviews++
		out := cleanReviewFindings()
		out.Items = []Finding{{Severity: "error", Action: types.ActionAutoFix, File: "feature.txt", Description: description}}
		raw, err := json.Marshal(out)
		return &agent.Result{Output: raw}, err
	}}
	sctx := newTestContextWithDBRecords(t, ag, dir, base, head, config.Commands{})
	sctx.Config.Review.DeferNonErrors = true
	executor, parks, done := startFollowUpReview(t, sctx, ag)
	gate := waitFollowUpReviewPark(t, parks, done)
	if err := executor.Respond(types.StepReview, types.ActionFix, []string{gate.Items[0].ID}); err != nil {
		t.Fatal(err)
	}
	gate = waitFollowUpReviewPark(t, parks, done)
	if len(gate.ReviewedPaths) != 0 ||
		!slices.ContainsFunc(gate.Items, func(f types.Finding) bool { return f.Severity == "error" && f.Description == description }) ||
		!slices.ContainsFunc(gate.Items, func(f types.Finding) bool { return f.ID == types.FindingIDFixRoundNoDiff }) {
		t.Fatalf("wrong gate after unchanged fix: %+v", gate)
	}
	if err := executor.Respond(types.StepReview, types.ActionApprove, nil); err != nil {
		t.Fatal(err)
	}
	waitFollowUpReviewDone(t, parks, done)
	run, err := sctx.DB.GetRun(sctx.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reviews != 1 || fixes != 1 || run.ReviewApprovedHeadSHA == nil || *run.ReviewApprovedHeadSHA != head {
		t.Fatalf("reviewers=%d, fixers=%d, approved head=%v, want the original review and one no-op fix of %s", reviews, fixes, run.ReviewApprovedHeadSHA, head)
	}
}

func startFollowUpReview(t *testing.T, sctx *pipeline.StepContext, ag agent.Agent) (*pipeline.Executor, <-chan string, <-chan error) {
	t.Helper()
	parks := make(chan string, 8)
	executor := pipeline.NewExecutor(sctx.DB, paths.WithRoot(t.TempDir()), sctx.Config, ag, []pipeline.Step{&ReviewStep{}}, func(event ipc.Event) {
		if event.Type == ipc.EventStepCompleted && event.Status != nil && event.Findings != nil &&
			(*event.Status == string(types.StepStatusAwaitingApproval) || *event.Status == string(types.StepStatusFixReview)) {
			parks <- *event.Findings
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	done := make(chan error, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		done <- executor.Execute(ctx, sctx.Run, sctx.Repo, sctx.WorkDir)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Error("Review executor did not exit after cancellation")
		}
	})
	return executor, parks, done
}

func waitFollowUpReviewPark(t *testing.T, parks <-chan string, done <-chan error) types.Findings {
	t.Helper()
	select {
	case raw := <-parks:
		gate, err := types.ParseFindingsJSON(raw)
		if err != nil {
			t.Fatal(err)
		}
		return gate
	case err := <-done:
		t.Fatalf("Review exited instead of parking: %v", err)
	case <-time.After(20 * time.Second):
		t.Fatal("Review did not park")
	}
	return types.Findings{}
}

func waitFollowUpReviewDone(t *testing.T, parks <-chan string, done <-chan error) {
	t.Helper()
	select {
	case raw := <-parks:
		t.Fatalf("Review parked instead of completing: %s", raw)
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Review did not complete")
	}
}
