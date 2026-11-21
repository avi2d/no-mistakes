package daemon

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/runenv"
)

func TestPipelineFixerTrialDoesNotMixModelsOrResumeTheBaseSession(t *testing.T) {
	global, err := config.LoadGlobalFromBytes([]byte(`agent: pi
review_agents:
  fixer: {agent: pi, model: base-fixer}
  fixer_after_round:
    agent: pi
    model: claude-bridge/claude-opus-5-5
    alternate: {agent: pi, model: openai/gpt-6.1-sol}
`))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ id, model string }{
		{"alpha", "claude-bridge/claude-opus-5-5"},
		{"delta", "openai/gpt-6.1-sol"},
	} {
		t.Run(tc.id, func(t *testing.T) {
			dir := t.TempDir()
			capture := filepath.Join(dir, "argv")
			cfg := config.Merge(global, &config.RepoConfig{})
			cfg.AgentPathOverride = map[string]string{"pi": writeCapturingPiAgent(t, dir, capture)}
			ag, _, err := newPipelineAgent(context.Background(), cfg, t.TempDir(), fakeLookPath, runenv.Overlay{}, nil, nil, tc.id)
			if err != nil {
				t.Fatal(err)
			}
			defer ag.Close()

			var recorded []string
			for _, round := range []int{1, 2, 5} {
				opts := agent.RunOpts{Purpose: "review-fix", Round: round, Prompt: "fix", CWD: dir}
				if round > 1 {
					opts.Session = &agent.SessionRef{Agent: "pi", ID: "e0000000-0000-4000-8000-000000000000"}
				}
				opts.OnAttempt = func(attempt agent.Attempt) {
					recorded = append(recorded, attempt.ConfiguredModel)
				}
				if _, err := ag.Run(context.Background(), opts); err != nil {
					t.Fatal(err)
				}
			}
			if want := []string{"", tc.model, tc.model}; !reflect.DeepEqual(recorded, want) {
				t.Fatalf("recorded round models = %q, want %q", recorded, want)
			}
			raw, err := os.ReadFile(capture + "." + filepath.Base(dir))
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
			if len(lines) != 3 {
				t.Fatalf("expected three Pi invocations; argv = %q", raw)
			}
			for i, model := range []string{"base-fixer", tc.model, tc.model} {
				if !strings.Contains(lines[i], "--model "+model+" ") || strings.Contains(lines[i], "--session") {
					t.Errorf("turn %d argv = %q; want model %s and no session", i+1, lines[i], model)
				}
			}
		})
	}
}
