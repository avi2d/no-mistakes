package steps

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/config"
)

func TestReviewPromptDoesNotParkIntentNarrowingOrAutofixProtectedDecisions(t *testing.T) {
	dir, baseSHA, headSHA := setupGitRepo(t)
	output, err := json.Marshal(cleanReviewFindings())
	if err != nil {
		t.Fatal(err)
	}
	ag := &mockAgent{name: "test", runFn: func(context.Context, agent.RunOpts) (*agent.Result, error) {
		return &agent.Result{Output: output}, nil
	}}
	sctx := newTestContextWithDBRecords(t, ag, dir, baseSHA, headSHA, config.Commands{})
	if _, err := (&ReviewStep{}).Execute(sctx); err != nil {
		t.Fatal(err)
	}
	if len(ag.calls) != 1 {
		t.Fatalf("got %d review calls, want 1", len(ag.calls))
	}
	prompt := ag.calls[0].Prompt
	for _, want := range []string{
		`action "auto-fix" when its only recommendation is to remove code, documentation, or behavior beyond the stated intent`,
		"public API or CLI surface",
		"security-sensitive path",
		"user-facing wording or article text",
		"choice between two product behaviors",
		`use "ask-user" when any of these exclusions applies or when in doubt`,
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("review prompt missing %q", want)
		}
	}
}
