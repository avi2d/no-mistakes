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

// repairChangedWorktree reports whether the fixer left anything a commit or
// push would carry: uncommitted changes or a moved HEAD.
func repairChangedWorktree(sctx *pipeline.StepContext) (bool, error) {
	head, err := stepGitHeadSHA(sctx)
	if err != nil {
		return false, fmt.Errorf("resolve head before pre-push checks: %w", err)
	}
	if !strings.EqualFold(head, sctx.Run.HeadSHA) {
		return true, nil
	}
	status, err := stepGitRun(sctx, "status", "--porcelain")
	if err != nil {
		return false, fmt.Errorf("check CI changes before pre-push checks: %w", err)
	}
	return strings.TrimSpace(status) != "", nil
}

// checkRepairBeforePush runs the pre-push checks on the fixer's worktree and,
// while they fail, hands their output back to the fixer for up to
// ciPrePushFixTurns more turns. It returns the last agent result, or the final
// *prePushCheckFailure once the turns are spent.
func (s *CIStep) checkRepairBeforePush(sctx *pipeline.StepContext, repairPrompt string, result *agent.Result) (*agent.Result, error) {
	if !prePushChecksConfigured(sctx) || ciRevalidatesRepairs(sctx) {
		return result, nil
	}
	for turn := 0; ; turn++ {
		changed, err := repairChangedWorktree(sctx)
		if err != nil {
			return result, err
		}
		if !changed {
			return result, nil
		}
		checkErr := runPrePushChecks(sctx)
		if checkErr == nil {
			s.prePushChecksPassed = true
			return result, nil
		}
		var failure *prePushCheckFailure
		if !errors.As(checkErr, &failure) || turn == ciPrePushFixTurns {
			return result, checkErr
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
			return result, fmt.Errorf("agent CI pre-push fix: %w", err)
		}
		result = next
		if err := refuseUnfinishedGitOperation(sctx); err != nil {
			return result, err
		}
	}
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
