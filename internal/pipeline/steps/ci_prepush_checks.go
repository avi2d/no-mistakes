package steps

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

// ciPrePushFixTurns bounds the extra fixer turns one CI repair attempt spends
// on its own pre-push check failures before the step parks.
const ciPrePushFixTurns = 2

const ciPrePushCheckFindingID = "ci-pre-push-checks"

// prePushCheckFailure is a pre-push check run that completed with at least one
// failing check. Output is already bounded for findings and prompts.
type prePushCheckFailure struct {
	Failed []checkResult
	Output string
}

func (f *prePushCheckFailure) Error() string {
	descriptions := make([]string, len(f.Failed))
	for i, result := range f.Failed {
		descriptions[i] = result.description("lint")
	}
	return "pre-push checks failed: " + strings.Join(descriptions, "; ")
}

// prePushChecksConfigured reports whether the Lint step has deterministic
// checks to re-run. An agent-only lint pass is not reproducible, so it is
// never re-run as a push guard.
func prePushChecksConfigured(sctx *pipeline.StepContext) bool {
	if sctx.Config == nil {
		return false
	}
	return strings.TrimSpace(sctx.Config.Commands.Lint) != "" || len(sctx.Config.CommandOverrides["lint"].Additional) > 0
}

// runPrePushChecks runs the Lint step's configured checks, commands.lint and
// the machine-local additional lint checks, in the run worktree. It never runs
// commands.test. A failing check returns *prePushCheckFailure. Any other error
// means the checks could not run.
func runPrePushChecks(sctx *pipeline.StepContext) error {
	if !prePushChecksConfigured(sctx) {
		return nil
	}
	if err := ensurePrepared(sctx, types.StepCI); err != nil {
		return fmt.Errorf("prepare pre-push checks: %w", err)
	}
	sctx.Log("running the configured lint checks before publishing the CI repair")
	output, results, err := runConfiguredChecks(sctx, "lint", strings.TrimSpace(sctx.Config.Commands.Lint))
	projected := logCommandOutput(sctx, output, "pre-push lint", types.StepCI)
	if err != nil {
		return fmt.Errorf("run pre-push checks: %w", err)
	}
	if failed := failedChecks(results); len(failed) > 0 {
		return &prePushCheckFailure{Failed: failed, Output: projected}
	}
	sctx.Log("pre-push checks passed")
	return nil
}

// The checks read HEAD, so the repair is committed before they run. A failed
// check rewinds that commit, so every fixer turn starts from the same head.
func (s *CIStep) commitRepairAfterPrePushChecks(sctx *pipeline.StepContext, repairPrompt, summary string, result *agent.Result) (ciRepairResult, error) {
	if !prePushChecksConfigured(sctx) || ciRevalidatesRepairs(sctx) {
		return s.commitRepair(sctx, summary, result)
	}
	for turn := 0; ; turn++ {
		startHead, err := stepGitHeadSHA(sctx)
		if err != nil {
			return ciRepairResult{}, fmt.Errorf("resolve head before CI repair commit: %w", err)
		}
		headSHA, record, err := s.commitRepairChanges(sctx, summary, result)
		if err != nil {
			return ciRepairResult{}, err
		}
		if !record {
			return ciRepairResult{}, nil
		}
		checkErr := runPrePushChecks(sctx)
		if checkErr == nil {
			s.prePushChecksPassed = true
			return s.recordRepair(sctx, headSHA)
		}
		if err := rewindRepairCommit(sctx, startHead, headSHA); err != nil {
			return ciRepairResult{}, errors.Join(checkErr, err)
		}
		var failure *prePushCheckFailure
		if !errors.As(checkErr, &failure) || turn == ciPrePushFixTurns {
			return ciRepairResult{}, checkErr
		}
		sctx.Log(fmt.Sprintf("pre-push checks failed, asking the CI fixer to fix them (turn %d of %d)...", turn+1, ciPrePushFixTurns))
		next, err := runScratchAwareAgent(sctx, agent.RunOpts{
			Prompt:     ciPrePushFixPrompt(repairPrompt, failure),
			CWD:        sctx.WorkDir,
			JSONSchema: ciFixConclusionSchema,
			OnChunk:    sctx.LogChunk,
		}, func(opts agent.RunOpts) (*agent.Result, error) {
			return sctx.RunAgentContext(sctx.Ctx, opts)
		})
		if err != nil {
			if !errors.Is(err, context.Canceled) && !errors.Is(err, pipeline.ErrAgentTimeout) {
				err = fmt.Errorf("%w: %w", errCIFixAgentCrash, err)
			}
			return ciRepairResult{}, fmt.Errorf("agent CI pre-push fix: %w", err)
		}
		result = next
		if err := refuseUnfinishedGitOperation(sctx); err != nil {
			return ciRepairResult{}, err
		}
	}
}

// The reset is mixed: the commit's changes stay in the worktree for the fixer.
func rewindRepairCommit(sctx *pipeline.StepContext, startHead, headSHA string) error {
	if strings.EqualFold(startHead, headSHA) {
		return nil
	}
	if _, err := stepGitRun(sctx, "reset", "-q", startHead); err != nil {
		return fmt.Errorf("rewind unchecked CI repair commit %s: %w", shortObjectID(headSHA), err)
	}
	return nil
}

func ciPrePushFixPrompt(repairPrompt string, failure *prePushCheckFailure) string {
	return fixerPrompt(fmt.Sprintf(`Your CI repair is already applied in this worktree, but the repository's configured lint checks fail on it, so the pipeline will not push it.
Fix only what these checks report, then return.

Rules:
- Any rebase or merge the repair needed is already concluded. Do not start, redo, or abort one.
- Do not run git commit: leave your fixes in the working tree and the pipeline commits them.
- Do not run the test suite.
- Re-run the failing checks before finishing.

Failing checks:
%s

Check output:
%s

The repair task you already applied, for context:
%s`, failure.Error(), failure.Output, repairPrompt))
}

// prePushCheckParkOutcome parks the CI step with the failing checks' output
// when a repair could not be made to pass them. The repair is not published.
func prePushCheckParkOutcome(failure *prePushCheckFailure, findings Findings, deferredRaw string) *pipeline.StepOutcome {
	withFailure := findings
	withFailure.Items = append(append([]Finding{}, findings.Items...), Finding{
		ID:          ciPrePushCheckFindingID,
		Severity:    types.FindingSeverityError,
		Description: failure.Error() + "; the CI repair was not pushed",
		Action:      types.ActionAskUser,
	})
	return ciRepairParkOutcome(withFailure, deferredRaw, failure.Output)
}
