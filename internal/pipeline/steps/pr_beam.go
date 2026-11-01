package steps

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/scm"
)

func (s *PRStep) draftPRContentWithBeam(sctx *pipeline.StepContext, prompt string, schema json.RawMessage, finalDiff, riskLine, testingMD, pipelineMD string, bodyLimit int, provider scm.Provider) (prContent, bool, error) {
	if !beamDraftsAllowed(sctx) {
		return prContent{}, false, nil
	}
	opts := agent.RunOpts{
		Prompt:     prompt,
		CWD:        sctx.WorkDir,
		JSONSchema: schema,
		OnChunk:    sctx.LogChunk,
	}
	result, err := runFreeModelTurn(sctx, agent.FreeModelBeam, opts)
	if err != nil {
		sctx.Log(fmt.Sprintf("beam PR draft failed (%s); retrying on the fallback model", err))
		result = nil
	}
	var content prContent
	var ok bool
	if result != nil {
		content, ok = parsePRDraftContent(sctx, result.Output)
	}
	if !ok {
		sctx.Log("beam PR draft unusable; retrying on the fallback model")
		if result, err = runFreeModelFallbackTurn(sctx, agent.FreeModelBeam, opts); err == nil {
			content, ok = parsePRDraftContent(sctx, result.Output)
		}
	}
	if !ok {
		sctx.Log("beam PR draft unusable; drafting on the configured model")
		return prContent{}, false, nil
	}
	title, err := renderPRTitle(sctx, content.Title)
	if err != nil {
		return prContent{}, false, nil
	}
	if err := acceptPRIntentBrief(sctx, content.IntentBrief); err != nil {
		return prContent{}, true, err
	}
	content.Title = title
	content.Body = neutralizeAttestationMarkers(provider, content.Body)
	if keepsPRUpdateText(sctx) {
		kept, err := s.publishKeptDraft(sctx, provider, content.Title, content.Body, bodyLimit)
		return kept, true, err
	}
	content.Body = assembleDraftPRBody(sctx, content.Body, riskLine, testingMD, pipelineMD, bodyLimit, provider)
	return content, true, nil
}

func (s *PRStep) draftConfiguredPRTitleWithBeam(sctx *pipeline.StepContext, prompt string, schema json.RawMessage) (string, bool) {
	if !beamDraftsAllowed(sctx) {
		return "", false
	}
	opts := agent.RunOpts{
		Prompt:     prompt,
		CWD:        sctx.WorkDir,
		JSONSchema: schema,
		OnChunk:    sctx.LogChunk,
	}
	result, err := runFreeModelTurn(sctx, agent.FreeModelBeam, opts)
	if err != nil {
		sctx.Log(fmt.Sprintf("beam PR title draft failed (%s); retrying on the fallback model", err))
		result = nil
	}
	var content prContent
	var ok bool
	if result != nil {
		content, ok = parsePRDraftTitle(sctx, result.Output)
	}
	if !ok {
		sctx.Log("beam PR title draft unusable; retrying on the fallback model")
		if result, err = runFreeModelFallbackTurn(sctx, agent.FreeModelBeam, opts); err == nil {
			content, ok = parsePRDraftTitle(sctx, result.Output)
		}
	}
	if !ok {
		sctx.Log("beam PR title draft unusable; drafting on the configured model")
		return "", false
	}
	rendered, err := renderPRTitle(sctx, content.Title)
	if err != nil {
		return "", false
	}
	if acceptPRIntentBrief(sctx, content.IntentBrief) != nil {
		return "", false
	}
	return rendered, true
}

func parsePRDraftContent(sctx *pipeline.StepContext, output []byte) (prContent, bool) {
	var content prContent
	if err := json.Unmarshal(output, &content); err != nil {
		return prContent{}, false
	}
	content.Title = strings.TrimSpace(content.Title)
	content.Body = strings.TrimSpace(content.Body)
	content.Body = unwrapNestedPRBody(content.Body)
	content.Body = stripGeneratedSections(content.Body)
	if content.Title == "" || content.Body == "" || !beamIntentBriefUsable(sctx, content.IntentBrief) {
		return prContent{}, false
	}
	return content, true
}

func parsePRDraftTitle(sctx *pipeline.StepContext, output []byte) (prContent, bool) {
	var content prContent
	if json.Unmarshal(output, &content) != nil {
		return prContent{}, false
	}
	content.Title = strings.TrimSpace(content.Title)
	if content.Title == "" || !beamIntentBriefUsable(sctx, content.IntentBrief) {
		return prContent{}, false
	}
	return content, true
}

// A rejected brief must send the draft to the fallback model; acceptPRIntentBrief
// would instead fail the step on what is only a weak free-model answer.
func beamIntentBriefUsable(sctx *pipeline.StepContext, brief *prIntentBrief) bool {
	if !needsPRIntentBrief(sctx) {
		return true
	}
	if brief == nil {
		return false
	}
	_, err := renderPRIntentBrief(*brief, prIntentBriefSections(sctx))
	return err == nil
}
