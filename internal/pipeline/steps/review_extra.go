package steps

import (
	"fmt"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

const extraReviewerSection = `

Extra-reviewer protocol (advisory, non-blocking):
- You are a second reviewer reading the same change. Report anything the first review may have missed.
- Do not edit, create, or delete any file, and do not run any command that changes the checkout. Read-only analysis.
- Return the same review JSON shape, with findings and the risk assessment filled in as usual.
`

// Only findings cross back; the extra turn never certifies the head.
func (s *ReviewStep) appendExtraReviewFindings(sctx *pipeline.StepContext, basePrompt string, workload *agent.InvocationWorkload) []types.Finding {
	if sctx.EvalReplay || !minimaxExtraReviewAllowed(sctx) {
		return nil
	}
	snap, err := snapshotWorktree(sctx.Ctx, sctx.WorkDir)
	if err != nil {
		sctx.Log(fmt.Sprintf("extra review skipped: %s", err))
		return nil
	}
	checkWorktree := func() bool {
		if !snap.mutated(sctx.Ctx, sctx.WorkDir) {
			return true
		}
		sctx.Log("extra review changed the checkout; its edits were discarded and its findings dropped")
		if err := snap.restore(sctx.Ctx, sctx.WorkDir); err != nil {
			sctx.Log(fmt.Sprintf("extra review restore: %s", err))
		}
		return false
	}
	opts := agent.RunOpts{
		Prompt:     basePrompt + extraReviewerSection,
		CWD:        sctx.WorkDir,
		Env:        sctx.Env,
		JSONSchema: reviewSchemaForFinalize(false),
		OnChunk:    sctx.LogChunk,
		Purpose:    "extra-review",
		Workload:   workload,
	}
	result, err := runFreeModelTurn(sctx, agent.FreeModelMinimax, opts)
	var extra Findings
	if err == nil {
		if extra, err = parseReviewAnalyzerOutput(result); err != nil {
			sctx.Log(fmt.Sprintf("extra review output unusable (%s); retrying on the fallback model", err))
			result = nil
		}
	} else {
		sctx.Log(fmt.Sprintf("extra review on the free model failed (%s); retrying on the fallback model", err))
		result = nil
	}
	if result == nil {
		sctx.Log("extra review unusable; retrying on the fallback model")
		if result, err = runFreeModelFallbackTurn(sctx, agent.FreeModelMinimax, opts); err != nil {
			sctx.Log(fmt.Sprintf("extra review skipped: fallback model failed (%s)", err))
			checkWorktree()
			return nil
		}
		if extra, err = parseReviewAnalyzerOutput(result); err != nil {
			sctx.Log(fmt.Sprintf("extra review skipped: fallback output unusable (%s)", err))
			checkWorktree()
			return nil
		}
	}
	if !checkWorktree() {
		return nil
	}
	if stripped, n := stripDeferredPipelineOwnedDeliveryFindings(extra); n > 0 {
		sctx.Log(fmt.Sprintf("dropped %d deferred pipeline-owned delivery finding(s) from the extra review", n))
		extra = stripped
	}
	items := stampExtraReviewFindings(extra.Items)
	if len(items) > 0 {
		sctx.Log(fmt.Sprintf("extra review contributed %d advisory finding(s) beside the normal review", len(items)))
	}
	return items
}
