package config

import (
	"strings"
	"testing"
)

const fixerTrialYAML = `review_agents:
  fixer_after_round:
    agent: pi
    model: claude-bridge/claude-opus-5-5
    alternate: {agent: pi, model: openai/gpt-6.1-sol, effort: high}
`

func TestFixerTrialDoesNotSwapArmsOrChangeTheTakeoverRound(t *testing.T) {
	global, err := LoadGlobalFromBytes([]byte(fixerTrialYAML))
	if err != nil {
		t.Fatal(err)
	}
	cfg := Merge(global, &RepoConfig{})
	for _, tc := range []struct {
		id, model, effort string
	}{
		{"alpha", "claude-bridge/claude-opus-5-5", ""},
		{"delta", "openai/gpt-6.1-sol", "high"},
		{"alpha", "claude-bridge/claude-opus-5-5", ""},
	} {
		got, ok := cfg.ReviewAgentForRun(RoleFixerAfterRound, tc.id)
		if !ok || got.Model != tc.model || string(got.Effort) != tc.effort || got.TakesOverFromRound() != 2 {
			t.Errorf("run %s got %+v; want %s with effort %q from round 2", tc.id, got, tc.model, tc.effort)
		}
	}
}

func TestFixerTrialRejectsAnAlternateOnOtherRolesOrAnIndistinctProfile(t *testing.T) {
	for _, body := range []string{
		strings.Replace(fixerTrialYAML, "fixer_after_round:", "reviewer_after_round:", 1),
		strings.Replace(fixerTrialYAML, "openai/gpt-6.1-sol", "claude-bridge/claude-opus-5-5", 1),
		strings.Replace(fixerTrialYAML, "model: openai/gpt-6.1-sol, ", "", 1),
		strings.Replace(fixerTrialYAML, "effort: high", "after_round: 2", 1),
	} {
		if _, err := LoadGlobalFromBytes([]byte(body)); err == nil {
			t.Errorf("accepted invalid trial:\n%s", body)
		}
	}
}

func TestFixerTrialIgnoresRepositoryOwnedAssignments(t *testing.T) {
	repo, err := LoadRepoFromBytes([]byte(fixerTrialYAML))
	if err != nil {
		t.Fatal(err)
	}
	cfg := Merge(DefaultGlobalConfig(), repo)
	if _, ok := cfg.ReviewAgentForRun(RoleFixerAfterRound, "delta"); ok {
		t.Fatal("repository config selected a trial profile")
	}
}
