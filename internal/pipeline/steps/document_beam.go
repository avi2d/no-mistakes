package steps

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

const beamDocumentProtocolSection = `

Draft protocol (this turn only):
- Do not edit, create, or delete any file. Return every edit you would make as a single unified diff in the "diff" field, with paths relative to the checkout root, exactly as git diff would print them.
- Base the diff on the current checkout state.
- When nothing needs changing, return an empty "diff" string.
- Fill in "findings" and "summary" exactly as the task below describes; the pipeline checks the diff mechanically and applies it itself.
`

// An unchecked draft's edits never reach another agent's pass: a failed
// restore stops the handoff with an error instead of passing dirty state on.
func discardBeamDirectEdits(sctx *pipeline.StepContext, snap worktreeSnapshot) (bool, error) {
	if !snap.mutated(sctx.Ctx, sctx.WorkDir) {
		return false, nil
	}
	sctx.Log("beam draft edited the checkout directly; its edits were discarded")
	if err := snap.restore(sctx.Ctx, sctx.WorkDir); err != nil {
		return false, fmt.Errorf("beam draft restore: %w", err)
	}
	return true, nil
}

// A Beam draft that parses still passes the mechanical patch check before use.
func (s *DocumentStep) executeBeamDocumentTurn(sctx *pipeline.StepContext, prompt string, schema []byte, purpose, headBefore string, combinedLint bool) (*pipeline.StepOutcome, bool, error) {
	snap, err := snapshotWorktree(sctx.Ctx, sctx.WorkDir)
	if err != nil {
		sctx.Log(fmt.Sprintf("beam draft skipped: %s", err))
		return nil, false, nil
	}
	opts := agent.RunOpts{
		Prompt:     prompt + beamDocumentProtocolSection,
		CWD:        sctx.WorkDir,
		Env:        sctx.Env,
		JSONSchema: beamDocumentSchema(schema),
		OnChunk:    sctx.LogChunk,
		Purpose:    purpose,
	}
	result, err := runFreeModelTurn(sctx, agent.FreeModelBeam, opts)
	var draft beamDocumentDraft
	if err == nil {
		if draft, err = parseBeamDocumentOutput(result.Output); err != nil {
			sctx.Log(fmt.Sprintf("beam draft unusable (%s); retrying on the fallback model", err))
			result = nil
		}
	} else {
		sctx.Log(fmt.Sprintf("beam draft failed (%s); retrying on the fallback model", err))
		result = nil
	}
	if result == nil {
		if _, err := discardBeamDirectEdits(sctx, snap); err != nil {
			return nil, false, err
		}
		sctx.Log("beam draft unusable; retrying on the fallback model")
		if result, err = runFreeModelFallbackTurn(sctx, agent.FreeModelBeam, opts); err != nil {
			sctx.Log(fmt.Sprintf("beam draft fallback failed (%s)", err))
			if _, restoreErr := discardBeamDirectEdits(sctx, snap); restoreErr != nil {
				return nil, false, restoreErr
			}
			return nil, false, nil
		}
		if draft, err = parseBeamDocumentOutput(result.Output); err != nil {
			sctx.Log(fmt.Sprintf("beam draft fallback unusable (%s)", err))
			if _, restoreErr := discardBeamDirectEdits(sctx, snap); restoreErr != nil {
				return nil, false, restoreErr
			}
			return nil, false, nil
		}
	}
	discarded, err := discardBeamDirectEdits(sctx, snap)
	if err != nil {
		return nil, false, err
	}
	if discarded {
		return nil, false, nil
	}
	encoded, err := types.MarshalFindingsJSON(draft.Findings)
	if err != nil {
		sctx.Log(fmt.Sprintf("beam draft findings unusable (%s)", err))
		return nil, false, nil
	}
	raw := []byte(encoded)
	if err := validateDocumentAnalyzerFindings(raw, &draft.Findings, false); err != nil {
		sctx.Log(fmt.Sprintf("beam draft findings invalid (%s)", err))
		return nil, false, nil
	}
	if diff := strings.TrimSpace(draft.Diff); diff != "" {
		if err := verifyBeamDiff(sctx.Ctx, sctx.WorkDir, draft.Diff); err != nil {
			sctx.Log(fmt.Sprintf("beam draft discarded: %s", err))
			return nil, false, nil
		}
		if err := applyBeamDiff(sctx.Ctx, sctx.WorkDir, draft.Diff); err != nil {
			sctx.Log(fmt.Sprintf("beam draft discarded: %s", err))
			return nil, false, nil
		}
		sctx.Log("beam draft passed the patch check and was applied")
	}
	commitSummary := extractDocumentSummary(raw, "")
	fallbackSummary := "update documentation"
	if combinedLint {
		fallbackSummary = "update documentation and fix lint"
	}
	committed, err := commitAgentFixesWithResult(sctx, s.Name(), commitSummary, fallbackSummary, result)
	if err != nil {
		return nil, false, nil
	}
	changesApplied := committed || sctx.Run.HeadSHA != headBefore
	if err := validateDocumentAnalyzerFindings(raw, &draft.Findings, changesApplied); err != nil {
		sctx.Log(fmt.Sprintf("beam draft findings invalid (%s)", err))
		return nil, false, nil
	}
	if combinedLint {
		return s.verifyBeamDocumentTurn(sctx, prompt, schema, purpose, headBefore, changesApplied)
	}
	return finishDocumentOutcome(sctx, draft.Findings, changesApplied, combinedLint)
}

func (s *DocumentStep) verifyBeamDocumentTurn(sctx *pipeline.StepContext, prompt string, schema []byte, purpose, headBefore string, changesApplied bool) (*pipeline.StepOutcome, bool, error) {
	findings, verified, err := s.runDocumentAnalyzerPass(sctx, prompt, schema, purpose, headBefore, true)
	if err != nil {
		sctx.Log(fmt.Sprintf("beam verification failed (%s); falling back to the configured model", err))
		return nil, false, nil
	}
	return finishDocumentOutcome(sctx, findings, changesApplied || verified, true)
}

func (s *DocumentStep) runDocumentAnalyzerPass(sctx *pipeline.StepContext, prompt string, schema []byte, purpose, headBefore string, combinedLint bool) (Findings, bool, error) {
	result, err := sctx.RunAgentContext(sctx.Ctx, agent.RunOpts{
		Prompt:     prompt,
		CWD:        sctx.WorkDir,
		JSONSchema: schema,
		OnChunk:    sctx.LogChunk,
		Purpose:    purpose,
	})
	if err != nil {
		return Findings{}, false, fmt.Errorf("agent document: %w", err)
	}
	commitSummary := extractDocumentSummary(result.Output, "")
	fallbackSummary := "update documentation"
	if combinedLint {
		fallbackSummary = "update documentation and fix lint"
	}
	// Edits commit before the output validates, so untrusted output never drops real edits.
	committed, err := commitAgentFixesWithResult(sctx, s.Name(), commitSummary, fallbackSummary, result)
	if err != nil {
		return Findings{}, false, err
	}
	changesApplied := committed || sctx.Run.HeadSHA != headBefore
	var findings Findings
	if result.Output == nil {
		return Findings{}, false, agent.RejectStructuredOutput(fmt.Errorf("document analyzer returned no structured findings"))
	} else if err := validateDocumentAnalyzerFindings(result.Output, &findings, changesApplied); err != nil {
		return Findings{}, false, agent.RejectStructuredOutput(fmt.Errorf("validate document analyzer findings: %w", err))
	}
	return findings, changesApplied, nil
}

func finishDocumentOutcome(sctx *pipeline.StepContext, findings Findings, changesApplied, combinedLint bool) (*pipeline.StepOutcome, bool, error) {
	docFindings := findings
	if combinedLint {
		var lintFindings Findings
		docFindings, lintFindings = splitHousekeepingFindings(findings)
		lintJSON, err := types.MarshalFindingsJSON(lintFindings)
		if err == nil {
			sctx.Shared.SetHousekeepingLint(pipeline.HousekeepingLintResult{
				FindingsJSON: lintJSON,
				Summary:      findings.Summary,
			})
			sctx.Log(fmt.Sprintf("housekeeping lint result recorded for the lint step: %d unresolved items", len(lintFindings.Items)))
		}
	}
	needsApproval := len(docFindings.Items) > 0
	findingsJSON, _ := json.Marshal(docFindings)
	sctx.Log(fmt.Sprintf("document findings: %d unresolved items", len(docFindings.Items)))
	return &pipeline.StepOutcome{
		NeedsApproval: needsApproval,
		AutoFixable:   false,
		Findings:      string(findingsJSON),
		FixSummary:    fixResultSummary(changesApplied),
	}, true, nil
}
