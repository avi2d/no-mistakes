package steps

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/commitlint"
	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/conventional"
	"github.com/kunchenguid/no-mistakes/internal/git"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
)

const maxCommitlintConfigBytes = 256 * 1024

// renderPRTitle formats title and holds it to the repository's commitlint
// rules: a title they reject gets one agent rewrite, and a rewrite they still
// reject fails the step instead of publishing a title CI would fail.
func renderPRTitle(sctx *pipeline.StepContext, title string) (string, error) {
	rendered, err := formatPRTitle(sctx, title)
	if err != nil {
		return "", err
	}
	lint := loadCommitlintConfig(sctx)
	if lint == nil {
		return rendered, nil
	}
	violations := lint.Lint(rendered, config.PRTitleSquashSuffixReserveChars)
	if len(violations) == 0 {
		return rendered, nil
	}
	sctx.Log(fmt.Sprintf("PR title %q breaks the commitlint rules in %s (%s); asking the agent to rewrite it", rendered, lint.Source, joinCommitlintViolations(violations)))
	rewritten, err := rewriteCommitlintPRTitle(sctx, lint, title, rendered, violations)
	if err != nil {
		return "", fmt.Errorf("PR title %q breaks the repository's commitlint rules in %s (%s), and the rewrite failed: %w", rendered, lint.Source, joinCommitlintViolations(violations), err)
	}
	if rendered, err = formatPRTitle(sctx, rewritten); err != nil {
		return "", err
	}
	if violations = lint.Lint(rendered, config.PRTitleSquashSuffixReserveChars); len(violations) > 0 {
		return "", fmt.Errorf("PR title %q still breaks the repository's commitlint rules in %s after one rewrite: %s", rendered, lint.Source, joinCommitlintViolations(violations))
	}
	return rendered, nil
}

func joinCommitlintViolations(violations []commitlint.Violation) string {
	parts := make([]string, len(violations))
	for i, v := range violations {
		parts[i] = v.String()
	}
	return strings.Join(parts, "; ")
}

func rewriteCommitlintPRTitle(sctx *pipeline.StepContext, lint *commitlint.Config, title, rendered string, violations []commitlint.Violation) (string, error) {
	var rules strings.Builder
	for _, v := range violations {
		fmt.Fprintf(&rules, "- %s\n", v)
	}
	shape := "- Keep a conventional-commit \"type(scope): description\" or \"type: description\" shape unless a rule above forbids it.\n" + conventional.ReleaseTypeRule
	if sctx.Config != nil && sctx.Config.PR.TitleFormat != "" {
		shape = fmt.Sprintf("- Return only the bare title text. The repository formatter turns your text into the published title, as it turned %q into %q; do not include its prefix or suffix yourself.", title, rendered)
	}
	length := ""
	if limit := lint.MaxHeaderLength(); limit > 0 {
		length = fmt.Sprintf("\n- The published title must be at most %d characters, because a squash merge appends up to %d more and the rule allows %d.", limit-config.PRTitleSquashSuffixReserveChars, config.PRTitleSquashSuffixReserveChars, limit)
	}
	prompt := fmt.Sprintf(`This pull request title breaks the repository's commitlint rules (%s). Rewrite it so it passes them.

Title:
%s

Broken rules:
%s
Rules:
- Return only the rewritten title text in the structured title field.
%s%s
- The rewrite must read as a complete description, not a truncation of the original.
- Do not invent behavior not implied by the original title.`, lint.Source, rendered, rules.String(), shape, length)
	result, err := sctx.RunAgentContext(sctx.Ctx, agent.RunOpts{
		Prompt:     prompt,
		CWD:        sctx.WorkDir,
		JSONSchema: prTitleSchema,
		OnChunk:    sctx.LogChunk,
	})
	if err != nil {
		return "", err
	}
	var content prContent
	if result == nil || json.Unmarshal(result.Output, &content) != nil || strings.TrimSpace(content.Title) == "" {
		return "", fmt.Errorf("agent returned no valid PR title rewrite")
	}
	return strings.TrimSpace(content.Title), nil
}

// loadCommitlintConfig returns the commitlint config at the root of the run's
// head commit, or nil when there is none whose rules can be read. It reads
// blobs from the commit rather than the worktree so a symlinked config cannot
// reach outside the repository.
func loadCommitlintConfig(sctx *pipeline.StepContext) *commitlint.Config {
	if sctx == nil || sctx.Run == nil || sctx.WorkDir == "" || !isHexObjectID(sctx.Run.HeadSHA) {
		return nil
	}
	args := append([]string{"ls-tree", "-z", sctx.Run.HeadSHA, "--"}, commitlint.SearchPlaces...)
	listing, err := git.RunRaw(sctx.Ctx, sctx.WorkDir, args...)
	if err != nil {
		sctx.Log(fmt.Sprintf("commitlint config not read, PR title not linted: %v", err))
		return nil
	}
	blobs := map[string]string{}
	for _, entry := range strings.Split(string(listing), "\x00") {
		metadata, name, ok := strings.Cut(entry, "\t")
		fields := strings.Fields(metadata)
		if ok && len(fields) == 3 && fields[1] == "blob" && (fields[0] == "100644" || fields[0] == "100755") {
			blobs[name] = fields[2]
		}
	}
	for _, name := range commitlint.SearchPlaces {
		oid, ok := blobs[name]
		if !ok {
			continue
		}
		cfg, err := readCommitlintConfig(sctx, name, oid)
		if err != nil {
			sctx.Log(fmt.Sprintf("commitlint config %s not read, PR title not linted: %v", name, err))
			return nil
		}
		if cfg != nil || !commitlint.IsConfigHolder(name) {
			return cfg
		}
	}
	return nil
}

func readCommitlintConfig(sctx *pipeline.StepContext, name, oid string) (*commitlint.Config, error) {
	sizeText, err := git.Run(sctx.Ctx, sctx.WorkDir, "cat-file", "-s", oid)
	if err != nil {
		return nil, err
	}
	if size, err := strconv.Atoi(sizeText); err != nil || size > maxCommitlintConfigBytes {
		return nil, fmt.Errorf("larger than %d bytes", maxCommitlintConfigBytes)
	}
	data, err := git.RunRaw(sctx.Ctx, sctx.WorkDir, "cat-file", "blob", oid)
	if err != nil {
		return nil, err
	}
	return commitlint.Parse(name, data)
}
