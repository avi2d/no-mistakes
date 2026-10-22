package daemon

import (
	"context"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/runenv"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func TestNewPipelineAgent_FreeModelsWrappedWhenEnabled(t *testing.T) {
	cfg := &config.Config{
		Agent: types.AgentClaude,
		MinimaxExtraReview: config.MinimaxExtraReview{
			Enabled: true, Model: "free-mini", FallbackModel: "fallback",
			Mode: "advisory", Steps: []string{"review"}, Repos: []string{"alpha"},
		},
		BeamDrafts: config.BeamDrafts{
			Enabled: true, Model: "free-beam", FallbackModel: "fallback",
			Steps: []string{"commit", "document"}, Repos: []string{"alpha"},
		},
	}
	ag, _, err := newPipelineAgent(context.Background(), cfg, t.TempDir(), fakeLookPath, runenv.Overlay{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ag.Close()
	runner, ok := ag.(agent.FreeModelRunner)
	if !ok {
		t.Fatal("pipeline agent with enabled free models does not serve free-model turns")
	}
	if _, err := runner.RunFreeModel(context.Background(), agent.FreeModelMinimax, agent.RunOpts{}); err == nil {
		t.Fatal("free turn with no built agent reported success")
	}
}

func TestNewPipelineAgent_FreeModelsPassThroughWhenDisabled(t *testing.T) {
	cfg := &config.Config{Agent: types.AgentClaude}
	ag, _, err := newPipelineAgent(context.Background(), cfg, t.TempDir(), fakeLookPath, runenv.Overlay{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ag.Close()
	if _, ok := ag.(agent.FreeModelRunner); ok {
		t.Fatal("pipeline agent with disabled free models wraps the primary agent")
	}
}
