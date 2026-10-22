package steps

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/scm"
)

func (s *PRStep) draftPRContentWithBeam(sctx *pipeline.StepContext, prompt, finalDiff, riskLine, testingMD, pipelineMD string, bodyLimit int, provider scm.Provider) (prContent, bool) {
	if !beamDraftsAllowed(sctx) {
		return prContent{}, false
	}
	opts := agent.RunOpts{
		Prompt:     prompt,
		CWD:        sctx.WorkDir,
		JSONSchema: prContentSchema,
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
		content, ok = parsePRDraftContent(result.Output)
	}
	if !ok {
		sctx.Log("beam PR draft unusable; retrying on the fallback model")
		if result, err = runFreeModelFallbackTurn(sctx, agent.FreeModelBeam, opts); err == nil {
			content, ok = parsePRDraftContent(result.Output)
		}
	}
	if !ok {
		sctx.Log("beam PR draft unusable; drafting on the configured model")
		return prContent{}, false
	}
	title, err := renderPRTitle(sctx, content.Title)
	if err != nil {
		return prContent{}, false
	}
	content.Title = title
	content.Body = neutralizeAttestationMarkers(provider, content.Body)
	content.Body = assembleDraftPRBody(sctx, content.Body, riskLine, testingMD, pipelineMD, bodyLimit, provider)
	return content, true
}

func (s *PRStep) draftConfiguredPRTitleWithBeam(sctx *pipeline.StepContext, prompt string) (string, bool) {
	if !beamDraftsAllowed(sctx) {
		return "", false
	}
	opts := agent.RunOpts{
		Prompt:     prompt,
		CWD:        sctx.WorkDir,
		JSONSchema: prTitleSchema,
		OnChunk:    sctx.LogChunk,
	}
	result, err := runFreeModelTurn(sctx, agent.FreeModelBeam, opts)
	if err != nil {
		sctx.Log(fmt.Sprintf("beam PR title draft failed (%s); retrying on the fallback model", err))
		result = nil
	}
	var title string
	var ok bool
	if result != nil {
		title, ok = parsePRDraftTitle(result.Output)
	}
	if !ok {
		sctx.Log("beam PR title draft unusable; retrying on the fallback model")
		if result, err = runFreeModelFallbackTurn(sctx, agent.FreeModelBeam, opts); err == nil {
			title, ok = parsePRDraftTitle(result.Output)
		}
	}
	if !ok {
		sctx.Log("beam PR title draft unusable; drafting on the configured model")
		return "", false
	}
	rendered, err := renderPRTitle(sctx, title)
	if err != nil {
		return "", false
	}
	return rendered, true
}

func parsePRDraftContent(output []byte) (prContent, bool) {
	var content prContent
	if err := json.Unmarshal(output, &content); err != nil {
		return prContent{}, false
	}
	content.Title = strings.TrimSpace(content.Title)
	content.Body = strings.TrimSpace(content.Body)
	content.Body = unwrapNestedPRBody(content.Body)
	content.Body = stripGeneratedSections(content.Body)
	if content.Title == "" || content.Body == "" {
		return prContent{}, false
	}
	return content, true
}

func parsePRDraftTitle(output []byte) (string, bool) {
	var content prContent
	if json.Unmarshal(output, &content) != nil {
		return "", false
	}
	if title := strings.TrimSpace(content.Title); title != "" {
		return title, true
	}
	return "", false
}
