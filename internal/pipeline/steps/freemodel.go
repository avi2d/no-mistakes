package steps

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/git"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func minimaxExtraReviewAllowed(sctx *pipeline.StepContext) bool {
	if sctx == nil || sctx.Config == nil || sctx.EvalReplay {
		return false
	}
	cfg := sctx.Config.MinimaxExtraReview
	if !cfg.Enabled {
		return false
	}
	return cfg.Allows(freeModelRepoURL(sctx))
}

func beamDraftsAllowed(sctx *pipeline.StepContext) bool {
	if sctx == nil || sctx.Config == nil {
		return false
	}
	return sctx.Config.BeamDrafts.Allows(freeModelRepoURL(sctx))
}

func freeModelRepoURL(sctx *pipeline.StepContext) string {
	if sctx == nil || sctx.Repo == nil {
		return ""
	}
	if url := strings.TrimSpace(sctx.Repo.UpstreamURL); url != "" {
		return url
	}
	return strings.TrimSpace(sctx.Repo.ForkURL)
}

func freeModelRunner(sctx *pipeline.StepContext) (agent.FreeModelRunner, bool) {
	if sctx == nil || sctx.Agent == nil {
		return nil, false
	}
	runner, ok := sctx.Agent.(agent.FreeModelRunner)
	return runner, ok
}

func freeModelTimeout(kind agent.FreeModelKind, cfg *config.Config) time.Duration {
	if kind == agent.FreeModelMinimax {
		return reviewAgentTimeout(cfg)
	}
	if cfg != nil && cfg.AgentTimeout > 0 {
		return cfg.AgentTimeout
	}
	return config.DefaultAgentTimeout
}

func runFreeModelTurn(sctx *pipeline.StepContext, kind agent.FreeModelKind, opts agent.RunOpts) (*agent.Result, error) {
	runner, ok := freeModelRunner(sctx)
	if !ok {
		return nil, fmt.Errorf("free model %q has no runner on this run", string(kind))
	}
	ctx, cancel := context.WithTimeout(sctx.Ctx, freeModelTimeout(kind, sctx.Config))
	defer cancel()
	return runner.RunFreeModel(ctx, kind, opts)
}

func runFreeModelFallbackTurn(sctx *pipeline.StepContext, kind agent.FreeModelKind, opts agent.RunOpts) (*agent.Result, error) {
	runner, ok := freeModelRunner(sctx)
	if !ok {
		return nil, fmt.Errorf("free model %q has no runner on this run", string(kind))
	}
	ctx, cancel := context.WithTimeout(sctx.Ctx, freeModelTimeout(kind, sctx.Config))
	defer cancel()
	return runner.RunFallbackModel(ctx, kind, opts)
}

// The downgrade is forced here, not in the prompt: a model that ignores the read-only rule still cannot park the gate.
func stampExtraReviewFindings(items []types.Finding) []types.Finding {
	stamped := make([]types.Finding, 0, len(items))
	for _, item := range items {
		item.ID = ""
		item.Severity = types.FindingSeverityInfo
		item.Action = types.ActionNoOp
		item.Category = types.FindingCategoryExtraReview
		if !strings.HasPrefix(item.Description, "Advisory:") {
			item.Description = "Advisory: " + strings.TrimSpace(item.Description)
		}
		if item.Source == "" {
			item.Source = "extra-review"
		}
		stamped = append(stamped, item)
	}
	return stamped
}

func countActionableReviewFindings(items []types.Finding) int {
	n := 0
	for _, item := range items {
		if item.Category != types.FindingCategoryExtraReview {
			n++
		}
	}
	return n
}

type worktreeSnapshot struct {
	head      string
	status    string
	untracked map[string]bool
}

func snapshotWorktree(ctx context.Context, dir string) (worktreeSnapshot, error) {
	var snap worktreeSnapshot
	head, err := git.Run(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return snap, fmt.Errorf("snapshot worktree head: %w", err)
	}
	status, err := git.Run(ctx, dir, "status", "--porcelain=v1", "-uall")
	if err != nil {
		return snap, fmt.Errorf("snapshot worktree status: %w", err)
	}
	snap.head = strings.TrimSpace(head)
	snap.status = status
	snap.untracked = map[string]bool{}
	for _, line := range strings.Split(status, "\n") {
		if strings.HasPrefix(line, "?? ") {
			snap.untracked[strings.TrimPrefix(line, "?? ")] = true
		}
	}
	return snap, nil
}

func (snap worktreeSnapshot) mutated(ctx context.Context, dir string) bool {
	head, err := git.Run(ctx, dir, "rev-parse", "HEAD")
	if err != nil || strings.TrimSpace(head) != snap.head {
		return true
	}
	status, err := git.Run(ctx, dir, "status", "--porcelain=v1", "-uall")
	if err != nil || status != snap.status {
		return true
	}
	return false
}

// Restore removes only paths that appeared after the snapshot; pre-existing state is never touched.
func (snap worktreeSnapshot) restore(ctx context.Context, dir string) error {
	if _, err := git.Run(ctx, dir, "reset", "--hard", snap.head); err != nil {
		return fmt.Errorf("restore worktree head: %w", err)
	}
	status, err := git.Run(ctx, dir, "status", "--porcelain=v1", "-uall")
	if err != nil {
		return fmt.Errorf("restore worktree status: %w", err)
	}
	for _, line := range strings.Split(status, "\n") {
		if !strings.HasPrefix(line, "?? ") {
			continue
		}
		path := strings.TrimPrefix(line, "?? ")
		if snap.untracked[path] {
			continue
		}
		if _, err := git.Run(ctx, dir, "clean", "-fd", "--", path); err != nil {
			return fmt.Errorf("restore worktree untracked %q: %w", path, err)
		}
	}
	return nil
}

type beamDocumentDraft struct {
	Diff     string
	Findings Findings
}

func beamDocumentSchema(active json.RawMessage) json.RawMessage {
	var schema map[string]any
	if err := json.Unmarshal(active, &schema); err != nil {
		return active
	}
	props, _ := schema["properties"].(map[string]any)
	if props == nil {
		return active
	}
	findings, _ := props["findings"].(map[string]any)
	summary, _ := props["summary"].(map[string]any)
	wrapped := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"diff":     map[string]any{"type": "string"},
			"findings": findings,
			"summary":  summary,
		},
		"required": []string{"diff", "findings", "summary"},
	}
	encoded, err := json.Marshal(wrapped)
	if err != nil {
		return active
	}
	return encoded
}

type beamDocumentEnvelope struct {
	Diff     string          `json:"diff"`
	Findings json.RawMessage `json:"findings"`
	Summary  string          `json:"summary"`
}

func parseBeamDocumentOutput(raw []byte) (beamDocumentDraft, error) {
	var draft beamDocumentDraft
	if len(bytes.TrimSpace(raw)) == 0 {
		return draft, fmt.Errorf("beam document draft is empty")
	}
	var envelope beamDocumentEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return draft, fmt.Errorf("beam document draft: %w", err)
	}
	draft.Diff = envelope.Diff
	if len(envelope.Findings) > 0 {
		combined, err := json.Marshal(map[string]any{
			"findings": json.RawMessage(envelope.Findings),
			"summary":  envelope.Summary,
		})
		if err != nil {
			return draft, fmt.Errorf("beam document draft: %w", err)
		}
		if err := unmarshalRequiredFindings(combined, &draft.Findings, false); err != nil {
			return draft, fmt.Errorf("beam document draft findings: %w", err)
		}
	}
	return draft, nil
}

func verifyBeamDiff(ctx context.Context, dir, diff string) error {
	if _, err := git.RunWithInput(ctx, dir, diff, "apply", "--check", "--recount", "-"); err != nil {
		return fmt.Errorf("beam diff does not apply: %w", err)
	}
	return nil
}

func applyBeamDiff(ctx context.Context, dir, diff string) error {
	if _, err := git.RunWithInput(ctx, dir, diff, "apply", "--recount", "-"); err != nil {
		return fmt.Errorf("apply beam diff: %w", err)
	}
	return nil
}
