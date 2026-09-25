package steps

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

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
