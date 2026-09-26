package steps

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
)

func renderTitleContext(pr config.PR) *pipeline.StepContext {
	return &pipeline.StepContext{
		Ctx:    context.Background(),
		Config: &config.Config{PR: pr},
		Run:    &db.Run{Branch: "refs/heads/feature"},
	}
}

func TestRenderPRTitle_ClampsOverLimitWithSuffixCounted(t *testing.T) {
	t.Parallel()

	sctx := renderTitleContext(config.PR{TitleMaxLength: 90})
	got, err := renderPRTitle(sctx, "feat(pipeline): add a squash-suffix-aware title budget with word-boundary shortening for long descriptions")
	if err != nil {
		t.Fatal(err)
	}
	if utf8.RuneCountInString(got)+10 > 90 {
		t.Fatalf("renderPRTitle() = %q, exceeds the header limit with the suffix counted", got)
	}
	if !strings.HasPrefix(got, "feat(pipeline): ") {
		t.Fatalf("renderPRTitle() cut the conventional type or scope: %q", got)
	}
}

func TestRenderPRTitle_LeavesShortTitleUntouched(t *testing.T) {
	t.Parallel()

	sctx := renderTitleContext(config.PR{TitleMaxLength: 90})
	title := "fix(ci): retry transient workflow failures"
	got, err := renderPRTitle(sctx, title)
	if err != nil {
		t.Fatal(err)
	}
	if got != title {
		t.Fatalf("renderPRTitle() = %q, want untouched %q", got, title)
	}
}

func TestRenderPRTitle_ClampsAfterTitleFormat(t *testing.T) {
	t.Parallel()

	sctx := renderTitleContext(config.PR{TitleFormat: "PROJ-123: {{.Title}}", TitleMaxLength: 60})
	got, err := renderPRTitle(sctx, "add a very long widget description that would overflow the header budget")
	if err != nil {
		t.Fatal(err)
	}
	if utf8.RuneCountInString(got)+10 > 60 {
		t.Fatalf("renderPRTitle() = %q, exceeds the header limit with the suffix counted", got)
	}
	if !strings.HasPrefix(got, "PROJ-123: ") {
		t.Fatalf("renderPRTitle() cut the formatted prefix: %q", got)
	}
}

// TestRenderPRTitle_RewritesOverLongTitleInsteadOfCuttingItMidPhrase pins the
// checks PR 62 regression: without a rewrite, ClampTitle cut this exact title
// (TitleMaxLength 100) to "...subsumes in a", which squash-merged into the
// published changelog. renderPRTitle must ask the agent for a complete
// rewrite instead of publishing that cut-off phrase.
func TestRenderPRTitle_RewritesOverLongTitleInsteadOfCuttingItMidPhrase(t *testing.T) {
	t.Parallel()

	ag := &mockAgent{
		name: "test",
		runFn: func(ctx context.Context, opts agent.RunOpts) (*agent.Result, error) {
			if !strings.Contains(opts.Prompt, "exceeds the repository's title length limit") {
				t.Errorf("rewrite prompt did not describe the over-long title, got: %s", opts.Prompt)
			}
			payload := json.RawMessage(`{"title":"feat(scripts): report tests a mutation run subsumes"}`)
			return &agent.Result{Output: payload}, nil
		},
	}
	sctx := renderTitleContext(config.PR{TitleMaxLength: 100})
	sctx.Agent = ag
	got, err := renderPRTitle(sctx, "feat(scripts): add checks-subsumed-tests to report tests another test subsumes in a bail-off mutation run")
	if err != nil {
		t.Fatal(err)
	}
	if len(ag.calls) != 1 {
		t.Fatalf("expected exactly one agent rewrite call, got %d", len(ag.calls))
	}
	if got != "feat(scripts): report tests a mutation run subsumes" {
		t.Fatalf("renderPRTitle() = %q, want the agent's rewrite used verbatim", got)
	}
	if utf8.RuneCountInString(got)+10 > 100 {
		t.Fatalf("renderPRTitle() = %q, exceeds the header limit with the suffix counted", got)
	}
}

func TestRenderPRTitle_RewriteStillOverLimitFallsBackToWordBoundaryClamp(t *testing.T) {
	t.Parallel()

	ag := &mockAgent{
		name: "test",
		runFn: func(ctx context.Context, opts agent.RunOpts) (*agent.Result, error) {
			payload := json.RawMessage(`{"title":"feat(scripts): this rewrite is still deliberately far too long to fit the configured budget"}`)
			return &agent.Result{Output: payload}, nil
		},
	}
	sctx := renderTitleContext(config.PR{TitleMaxLength: 90})
	sctx.Agent = ag
	got, err := renderPRTitle(sctx, "feat(scripts): add checks-subsumed-tests to report tests another test subsumes in a bail-off mutation run")
	if err != nil {
		t.Fatal(err)
	}
	if utf8.RuneCountInString(got)+10 > 90 {
		t.Fatalf("renderPRTitle() = %q, exceeds the header limit after falling back to clamping", got)
	}
	if !strings.HasPrefix(got, "feat(scripts): ") {
		t.Fatalf("renderPRTitle() cut the conventional type or scope: %q", got)
	}
}

func TestTitleAlreadyFitsWithinBudget(t *testing.T) {
	t.Parallel()

	sctx := renderTitleContext(config.PR{TitleMaxLength: 90})
	if !titleAlreadyFitsWithinBudget(sctx, "fix(ci): retry transient workflow failures") {
		t.Fatal("titleAlreadyFitsWithinBudget() = false for a short conventional title")
	}
	if titleAlreadyFitsWithinBudget(sctx, "update pull request") {
		t.Fatal("titleAlreadyFitsWithinBudget() = true for a non-conventional title")
	}
	if titleAlreadyFitsWithinBudget(sctx, "feat(scripts): "+strings.Repeat("word ", 30)) {
		t.Fatal("titleAlreadyFitsWithinBudget() = true for a title over budget")
	}
}

func TestRenderPRTitle_UnlimitedByDefault(t *testing.T) {
	t.Parallel()

	sctx := renderTitleContext(config.PR{})
	title := "feat(pipeline): " + strings.TrimSpace(strings.Repeat("word ", 30))
	got, err := renderPRTitle(sctx, title)
	if err != nil {
		t.Fatal(err)
	}
	if got != title {
		t.Fatalf("renderPRTitle() shortened without a configured limit")
	}
}
